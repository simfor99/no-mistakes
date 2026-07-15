package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
)

func TestRenderWatchResultUsesChecksPassedAsCanonicalHandoff(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := renderWatchResult(cmd, runView{ID: "run-1", Status: string(types.RunRunning)}, "checks-passed")
	if err != nil {
		t.Fatalf("renderWatchResult() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"stop: checks-passed",
		"terminal: false",
		"outcome: checks-passed",
		"supervision: active_agent_required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("watch output missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderWatchResultDoesNotInventMergeOutcome(t *testing.T) {
	for _, reason := range []string{"gate", "quiet"} {
		t.Run(reason, func(t *testing.T) {
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)

			err := renderWatchResult(cmd, runView{ID: "run-1", Status: string(types.RunRunning)}, reason)
			if err != nil {
				t.Fatalf("renderWatchResult() error = %v", err)
			}
			if strings.Contains(out.String(), "outcome: checks-passed") {
				t.Errorf("%s output must not contain checks-passed outcome:\n%s", reason, out.String())
			}
		})
	}
}
