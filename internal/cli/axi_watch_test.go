package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
)

func TestParseWatchUntilDefaultsToAttentionAndRejectsUnknown(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    watchUntil
		wantErr bool
	}{
		{name: "default", want: watchUntilAttention},
		{name: "attention", input: "attention", want: watchUntilAttention},
		{name: "terminal", input: "terminal", want: watchUntilTerminal},
		{name: "unknown", input: "forever", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWatchUntil(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseWatchUntil() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchUntil() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseWatchUntil() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWatchQuietDelayUsesNearestActiveStep(t *testing.T) {
	previous := watchNow
	watchNow = func() time.Time { return time.Unix(1_000, 0) }
	t.Cleanup(func() { watchNow = previous })

	first, second := int64(995), int64(997)
	rv := runView{Steps: []stepView{
		{Name: "review", Status: "running", LastActivityAt: &first},
		{Name: "test", Status: "fixing", LastActivityAt: &second},
	}}

	if got := watchQuietDelay(rv, 10*time.Second); got != 5*time.Second {
		t.Fatalf("watchQuietDelay() = %v, want 5s", got)
	}
}

func TestRenderWatchResultBoundsGateFindings(t *testing.T) {
	items := make([]types.Finding, maxWatchFindings+1)
	for i := range items {
		items[i] = types.Finding{ID: fmt.Sprintf("F%d", i+1), Severity: "medium", File: "file.go", Action: string(types.ActionFix), Description: "needs attention"}
	}
	findingsJSON, err := types.MarshalFindingsJSON(types.Findings{Items: items})
	if err != nil {
		t.Fatalf("encode findings: %v", err)
	}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err = renderWatchResult(cmd, runView{
		ID:     "run-1",
		Status: "running",
		Steps:  []stepView{{Name: string(types.StepReview), Status: string(types.StepStatusAwaitingApproval), FindingsJSON: findingsJSON}},
	}, "gate")
	if err != nil {
		t.Fatalf("renderWatchResult() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{"findings_total: 11", "findings_truncated: true", "F10", "no-mistakes axi logs --step review --full"} {
		if !strings.Contains(got, want) {
			t.Errorf("watch output missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "F11") {
		t.Errorf("watch output included an unbounded finding:\n%s", got)
	}
}

func TestRenderWatchResultIncludesTerminalError(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := renderWatchResult(cmd, runView{ID: "run-1", Status: "failed", Error: "review failed"}, "terminal")
	if err == nil {
		t.Fatal("failed terminal run must return an error exit")
	}
	for _, want := range []string{"outcome: failed", "error: review failed", "stop: terminal", "supervision: active_agent_required"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("watch output missing %q in:\n%s", want, out.String())
		}
	}
}
