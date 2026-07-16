package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/supervision"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestCanonicalSupervisorCWD(t *testing.T) {
	got, err := canonicalSupervisorCWD(".")
	if err != nil {
		t.Fatalf("canonicalSupervisorCWD() error = %v", err)
	}
	if got == "." || !strings.HasPrefix(got, "/") {
		t.Fatalf("canonicalSupervisorCWD() = %q, want absolute clean path", got)
	}
}

func TestCodexHookIgnoresNonStopEvents(t *testing.T) {
	if err := runAxiCodexHook(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"s","turn_id":"t","cwd":"/tmp"}`), io.Discard); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestCodexHookIgnoresMalformedPayload(t *testing.T) {
	if err := runAxiCodexHook(strings.NewReader(`not json`), io.Discard); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestClassifySupervisorRunFailsClosedForAskUserAndMalformedGates(t *testing.T) {
	findings := `{"findings":[{"id":"decision","action":"ask-user"}]}`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "ask user", raw: findings},
		{name: "malformed", raw: `{`},
		{name: "missing action", raw: `{"findings":[{"id":"unknown"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			run := &ipc.RunInfo{ID: "run", Status: types.RunRunning, AwaitingAgent: true, Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: types.StepStatusAwaitingApproval, FindingsJSON: &raw}}}
			if got := classifySupervisorRun(run, func(string) []string { return nil }); got != supervisorAskUser {
				t.Fatalf("classifySupervisorRun() = %q, want %q", got, supervisorAskUser)
			}
		})
	}
}

func TestClassifySupervisorRunRecognizesTechnicalGateAndTerminal(t *testing.T) {
	technical := `{"findings":[{"id":"fix","action":"auto-fix"}]}`
	run := &ipc.RunInfo{ID: "run", Status: types.RunRunning, AwaitingAgent: true, Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: types.StepStatusAwaitingApproval, FindingsJSON: &technical}}}
	if got := classifySupervisorRun(run, func(string) []string { return nil }); got != supervisorTechnicalGate {
		t.Fatalf("technical gate = %q, want %q", got, supervisorTechnicalGate)
	}
	run.Status = types.RunCompleted
	if got := classifySupervisorRun(run, func(string) []string { return nil }); got != supervisorTerminal {
		t.Fatalf("terminal = %q, want %q", got, supervisorTerminal)
	}
}

func TestApplySupervisorOutcomeBoundsReasonsAndPausesAfterStaleBudget(t *testing.T) {
	for _, budget := range []int{1, 4, 6} {
		t.Run("budget-"+string(rune('0'+budget)), func(t *testing.T) {
			nmHome := t.TempDir()
			p := paths.WithRoot(nmHome)
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nmHome, "config.yaml"), []byte("supervision_max_stale_heartbeats: "+string(rune('0'+budget))+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := supervision.NewStore(p.SupervisionDir())
			if _, err := store.Arm(supervision.Registration{RunID: "run", RepoID: "repo", CWD: "/work"}); err != nil {
				t.Fatal(err)
			}
			reg, ok, err := store.Claim("/work", "session")
			if err != nil || !ok {
				t.Fatalf("Claim() = (%+v, %v, %v)", reg, ok, err)
			}
			run := &ipc.RunInfo{ID: "run", RepoID: "repo", Status: types.RunRunning, UpdatedAt: 7}
			reg.Fingerprint = supervisorProgressFingerprint(run)
			if err := store.Save(reg); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= budget+1; i++ {
				reg, ok, err = store.Get("run")
				if err != nil || !ok {
					t.Fatalf("Get() = (%+v, %v, %v)", reg, ok, err)
				}
				var out bytes.Buffer
				applySupervisorOutcome(store, p, reg, codexHookEvent{SessionID: "session", TurnID: "turn-" + string(rune('0'+i))}, supervisorHeartbeat, run, &out)
				want := `{"decision":"block","reason":"nm_event=heartbeat"}` + "\n"
				if i == budget+1 {
					want = `{"decision":"block","reason":"nm_event=stale"}` + "\n"
				}
				if got := out.String(); got != want {
					t.Fatalf("heartbeat %d output = %q, want %q", i, got, want)
				}
			}
			reg, ok, err = store.Get("run")
			if err != nil || !ok || reg.Phase != supervision.PhasePaused {
				t.Fatalf("final registration = (%+v, %v, %v), want paused", reg, ok, err)
			}
		})
	}
}
