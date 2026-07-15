package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
)

func TestWaitForWatchSignalSkipsLogChunks(t *testing.T) {
	events := make(chan ipc.Event, 2)
	events <- ipc.Event{Type: ipc.EventLogChunk}
	events <- ipc.Event{Type: ipc.EventRunUpdated}
	if got := waitForWatchSignal(context.Background(), events, nil); got != watchSignalEvent {
		t.Fatalf("waitForWatchSignal() = %v, want event after log chunk", got)
	}
}

func TestWaitForWatchSignalKeepsTimerAfterLogChunk(t *testing.T) {
	events := make(chan ipc.Event, 1)
	events <- ipc.Event{Type: ipc.EventLogChunk}
	timer := make(chan time.Time, 1)
	timer <- time.Now()
	if got := waitForWatchSignal(context.Background(), events, timer); got != watchSignalTimer {
		t.Fatalf("waitForWatchSignal() = %v, want timer after log chunk", got)
	}
}

func TestLatchWatchAttention(t *testing.T) {
	for _, tc := range []struct {
		until  watchUntil
		reason string
		want   bool
	}{
		{watchUntilAttention, "checks-passed", false},
		{watchUntilTerminal, "checks-passed", true},
		{watchUntilTerminal, "gate", true},
		{watchUntilTerminal, "quiet", true},
		{watchUntilTerminal, "terminal", false},
	} {
		if got := latchWatchAttention(tc.until, tc.reason); got != tc.want {
			t.Errorf("latchWatchAttention(%q, %q) = %t, want %t", tc.until, tc.reason, got, tc.want)
		}
	}
}

func TestReconcileClosedWatchStreamPrefersSignalInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	called := false

	err := reconcileClosedWatchStream(cmd, ctx, func() (*ipc.RunInfo, error) {
		called = true
		return nil, errors.New("must not read")
	})
	if called {
		t.Fatal("reconcile read after signal cancellation")
	}
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("reconcileClosedWatchStream() error = %v, want exit 130", err)
	}
	if !strings.Contains(out.String(), "stop: interrupted") {
		t.Fatalf("watch output = %q, want interrupted result", out.String())
	}
}

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

func TestRenderWatchResultPreservesTerminalError(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := renderWatchResult(cmd, runView{
		ID:     "run-1",
		Status: string(types.RunFailed),
		Error:  "CI workflow failed",
	}, "terminal")
	if err == nil {
		t.Fatal("renderWatchResult() error = nil, want non-zero terminal error")
	}

	got := out.String()
	for _, want := range []string{"stop: terminal", "outcome: failed", "error: CI workflow failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal watch output missing %q in:\n%s", want, got)
		}
	}
}
