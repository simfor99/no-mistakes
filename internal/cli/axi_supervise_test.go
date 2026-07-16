package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestCanonicalSupervisorCWDNormalizesSubdirectoryToGitRoot(t *testing.T) {
	repoDir := setupTestRepo(t)
	subdir := filepath.Join(repoDir, "nested", "working-directory")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := canonicalSupervisorCWD(subdir)
	if err != nil {
		t.Fatalf("canonicalSupervisorCWD() error = %v", err)
	}
	want, err := canonicalSupervisorCWD(repoDir)
	if err != nil {
		t.Fatalf("canonicalSupervisorCWD(repo root) error = %v", err)
	}
	if got != want {
		t.Fatalf("canonicalSupervisorCWD(subdirectory) = %q, want git root %q", got, want)
	}
}

func TestSupervisionBranchMatchesCurrentWorktree(t *testing.T) {
	repoDir := setupTestRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/current")
	if !supervisionBranchMatches(context.Background(), repoDir, "feature/current") {
		t.Fatal("supervisionBranchMatches() = false, want current branch match")
	}
	if supervisionBranchMatches(context.Background(), repoDir, "feature/other") {
		t.Fatal("supervisionBranchMatches() = true, want branch mismatch rejection")
	}
}

func TestSupervisorHeartbeatDeadlineReusesFutureRegistrationDeadline(t *testing.T) {
	previous := supervisionNow
	supervisionNow = func() time.Time { return time.Unix(1_000, 0) }
	t.Cleanup(func() { supervisionNow = previous })

	if got := supervisorHeartbeatDeadline(supervision.Registration{NextHeartbeatAt: 1_030}); !got.Equal(time.Unix(1_030, 0)) {
		t.Fatalf("supervisorHeartbeatDeadline() = %v, want existing deadline", got)
	}
	if got := supervisorHeartbeatDeadline(supervision.Registration{}); !got.Equal(time.Unix(1_300, 0)) {
		t.Fatalf("supervisorHeartbeatDeadline() = %v, want five-minute deadline", got)
	}
}

func TestAxiSuperviseStatusRendersPersistedRegistration(t *testing.T) {
	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New() error = %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	if _, err := supervision.NewStore(p.SupervisionDir()).Arm(supervision.Registration{
		RunID: "run-supervise-status", RepoID: "repo-status", CWD: "/work/status",
	}); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}

	output, err := executeCmd("axi", "supervise", "status", "--run", "run-supervise-status")
	if err != nil {
		t.Fatalf("axi supervise status error = %v", err)
	}
	for _, want := range []string{
		"supervision: armed",
		"run_id: run-supervise-status",
		"session_bound: false",
		"stale_heartbeats: 0",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("axi supervise status output missing %q in:\n%s", want, output)
		}
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

func TestClaudeHookIgnoresMalformedAndNonStopPayloads(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/tmp","last_assistant_message":"done"}`,
		`{"hook_event_name":"Stop","session_id":"s","cwd":"/tmp"}`,
	} {
		if err := runAxiClaudeHook(strings.NewReader(raw), io.Discard); err != nil {
			t.Fatalf("runAxiClaudeHook(%q) error = %v", raw, err)
		}
	}
}

func TestClaudeTranscriptHandoffIDDistinguishesTurnsAndSuppressesDuplicates(t *testing.T) {
	previousWait := claudeTranscriptWait
	claudeTranscriptWait = 0
	t.Cleanup(func() { claudeTranscriptWait = previousWait })

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	boundary, err := claudeTranscriptBoundaryOffset(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","uuid":"assistant-1","message":{"content":"Done."}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, nextOffset := claudeTranscriptHandoffID("session-1", transcript, "Done.", boundary, "")
	duplicate, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", nextOffset, first)
	if first == "" || first != duplicate {
		t.Fatalf("same Claude Stop IDs = %q, %q; want identical non-empty opaque IDs", first, duplicate)
	}
	file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"assistant","uuid":"assistant-2","message":{"content":"Done."}}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", nextOffset, first)
	if second == "" || first == second {
		t.Fatalf("successive same-message Claude Stop IDs = %q, %q; want distinct IDs", first, second)
	}
	if strings.Contains(first, "session-1") || strings.Contains(first, "assistant-1") {
		t.Fatalf("claude hook ID leaks hook payload: %q", first)
	}
	if got, _ := claudeTranscriptHandoffID("session-1", filepath.Join(t.TempDir(), "missing.jsonl"), "Done.", 0, ""); got != "" {
		t.Fatalf("missing transcript ID = %q, want empty", got)
	}
}

func TestClaudeTranscriptHandoffIDIgnoresPreArmIdenticalAssistantMessage(t *testing.T) {
	previousWait := claudeTranscriptWait
	claudeTranscriptWait = 0
	t.Cleanup(func() { claudeTranscriptWait = previousWait })

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","uuid":"stale","message":{"content":[{"type":"text","text":"Done."}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundary, err := claudeTranscriptBoundaryOffset(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", boundary, ""); got != "" {
		t.Fatalf("pre-arm transcript handoff = %q, want empty", got)
	}
	file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"assistant","uuid":"current","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", boundary, ""); got == "" {
		t.Fatal("post-arm transcript handoff = empty, want matching assistant ID")
	}
}

func TestClaudeTranscriptHandoffIDWaitsForPostArmTranscriptFlush(t *testing.T) {
	previousWait := claudeTranscriptWait
	previousPoll := claudeTranscriptPollInterval
	claudeTranscriptWait = 300 * time.Millisecond
	claudeTranscriptPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		claudeTranscriptWait = previousWait
		claudeTranscriptPollInterval = previousPoll
	})

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	boundary, err := claudeTranscriptBoundaryOffset(transcript)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(25 * time.Millisecond)
		file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = file.WriteString(`{"type":"assistant","uuid":"current","message":{"content":"Done."}}` + "\n")
		_ = file.Close()
	}()
	if got, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", boundary, ""); got == "" {
		t.Fatal("delayed transcript handoff = empty, want matching assistant ID")
	}
}

func TestClaudeTranscriptHandoffIDScansPastLargePrefix(t *testing.T) {
	previousWait := claudeTranscriptWait
	previousChunk := claudeTranscriptScanChunk
	previousLimit := claudeTranscriptScanLimit
	claudeTranscriptWait = 0
	claudeTranscriptScanChunk = 64
	claudeTranscriptScanLimit = 512
	t.Cleanup(func() {
		claudeTranscriptWait = previousWait
		claudeTranscriptScanChunk = previousChunk
		claudeTranscriptScanLimit = previousLimit
	})

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := `{"type":"user","uuid":"prefix","message":{"content":"` + strings.Repeat("x", 180) + `"}}` + "\n"
	assistant := `{"type":"assistant","uuid":"current","message":{"content":"Done."}}` + "\n"
	if err := os.WriteFile(transcript, []byte(prefix+assistant), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := claudeTranscriptHandoffID("session-1", transcript, "Done.", 0, ""); got == "" {
		t.Fatal("large-prefix transcript handoff = empty, want matching assistant ID")
	}
}

func TestClaudeTranscriptHandoffPersistsVerifiedProgressPastScanLimit(t *testing.T) {
	previousWait := claudeTranscriptWait
	previousChunk := claudeTranscriptScanChunk
	previousLimit := claudeTranscriptScanLimit
	claudeTranscriptWait = 0
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := `{"type":"user","uuid":"prefix","message":{"content":"` + strings.Repeat("x", 200) + `"}}` + "\n"
	claudeTranscriptScanChunk = int64(len(prefix))
	claudeTranscriptScanLimit = int64(len(prefix))
	t.Cleanup(func() {
		claudeTranscriptWait = previousWait
		claudeTranscriptScanChunk = previousChunk
		claudeTranscriptScanLimit = previousLimit
	})

	assistant := `{"type":"assistant","uuid":"current","message":{"content":"Done."}}` + "\n"
	if err := os.WriteFile(transcript, []byte(prefix+assistant), 0o600); err != nil {
		t.Fatal(err)
	}
	first := claudeTranscriptHandoffForCursor("session-1", transcript, "Done.", 0, 0, "")
	if first.handoffID != "" || first.overflow || first.scanOffset != int64(len(prefix)) {
		t.Fatalf("first bounded scan = %+v, want verified prefix progress", first)
	}
	store := supervision.NewStore(t.TempDir())
	if _, err := store.Arm(supervision.Registration{RunID: "run", RepoID: "repo", CWD: "/work", ClaudeTranscriptBound: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim("/work", "session-1"); err != nil || !ok {
		t.Fatalf("Claim() = (_, %v, %v)", ok, err)
	}
	if _, ok, err := store.UpdateForSession("run", "session-1", func(reg *supervision.Registration) {
		reg.AdvanceClaudeTranscriptScanOffset(first.scanOffset)
	}); err != nil || !ok {
		t.Fatalf("UpdateForSession() = (_, %v, %v)", ok, err)
	}
	reg, ok, err := store.Get("run")
	if err != nil || !ok || reg.ClaudeTranscriptScanOffset != first.scanOffset {
		t.Fatalf("persisted scan progress = (%+v, %v, %v)", reg, ok, err)
	}
	second := claudeTranscriptHandoffForCursor("session-1", transcript, "Done.", 0, reg.ClaudeTranscriptScanOffset, "")
	if second.handoffID == "" || second.assistantOffset <= first.scanOffset {
		t.Fatalf("resumed bounded scan = %+v, want assistant handoff after progress", second)
	}
}

func TestClaudeTranscriptHandoffDoesNotAdvanceOnDelayedDuplicate(t *testing.T) {
	previousWait := claudeTranscriptWait
	claudeTranscriptWait = 0
	t.Cleanup(func() { claudeTranscriptWait = previousWait })

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	first := `{"type":"assistant","uuid":"first","message":{"content":"First."}}` + "\n"
	second := `{"type":"assistant","uuid":"second","message":{"content":"Second."}}` + "\n"
	if err := os.WriteFile(transcript, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(first))
	previous := claudeHookHandoffID("session-1", "first")
	handoff := claudeTranscriptHandoffForCursor("session-1", transcript, "First.", offset, offset, previous)
	if handoff.handoffID != previous || handoff.scanOffset != offset || handoff.overflow {
		t.Fatalf("delayed duplicate handoff = %+v, want unchanged cursor fallback", handoff)
	}
}

func TestClaudeTranscriptHandoffMarksOversizedRecordUnverifiable(t *testing.T) {
	previousWait := claudeTranscriptWait
	previousChunk := claudeTranscriptScanChunk
	previousLimit := claudeTranscriptScanLimit
	previousRecordLimit := claudeTranscriptRecordLimit
	claudeTranscriptWait = 0
	claudeTranscriptScanChunk = 32
	claudeTranscriptScanLimit = 256
	claudeTranscriptRecordLimit = 64
	t.Cleanup(func() {
		claudeTranscriptWait = previousWait
		claudeTranscriptScanChunk = previousChunk
		claudeTranscriptScanLimit = previousLimit
		claudeTranscriptRecordLimit = previousRecordLimit
	})

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	record := `{"type":"assistant","uuid":"current","message":{"content":"Done."},"extra":"` + strings.Repeat("x", 200) + `"}` + "\n"
	if err := os.WriteFile(transcript, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := claudeTranscriptHandoffForCursor("session-1", transcript, "Done.", 0, 0, "")
	if handoff.handoffID != "" || !handoff.unverifiable || handoff.scanOffset != 0 {
		t.Fatalf("oversized record handoff = %+v, want unverifiable paused-state input", handoff)
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

func TestClassifySupervisorRunUsesPersistedCIReadiness(t *testing.T) {
	run := &ipc.RunInfo{
		ID:      "run",
		Status:  types.RunRunning,
		CIReady: true,
		Steps:   []ipc.StepResultInfo{{StepName: types.StepCI, Status: types.StepStatusRunning}},
	}
	if got := classifySupervisorRun(run, func(string) []string { return nil }); got != supervisorChecksPassed {
		t.Fatalf("classifySupervisorRun() = %q, want %q", got, supervisorChecksPassed)
	}
}

func TestApplySupervisorOutcomeDeduplicatesRepeatedStopEvent(t *testing.T) {
	previous := supervisionNow
	supervisionNow = func() time.Time { return time.Unix(1_000, 0) }
	t.Cleanup(func() { supervisionNow = previous })

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
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
	event := supervisorHookEvent{SessionID: "session", HandoffID: "turn"}

	var first bytes.Buffer
	applySupervisorOutcome(store, p, reg, event, supervisorHeartbeat, run, &first)
	if got, want := first.String(), `{"decision":"block","reason":"nm_event=heartbeat"}`+"\n"; got != want {
		t.Fatalf("first Stop output = %q, want %q", got, want)
	}
	reg, ok, err = store.Get("run")
	if err != nil || !ok {
		t.Fatalf("Get() = (%+v, %v, %v)", reg, ok, err)
	}
	var repeated bytes.Buffer
	applySupervisorOutcome(store, p, reg, event, supervisorHeartbeat, run, &repeated)
	if got := repeated.String(); got != "" {
		t.Fatalf("repeated Stop output = %q, want no continuation", got)
	}
}

func TestApplySupervisorOutcomeAskUserAdvancesClaudeTranscriptOffset(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store := supervision.NewStore(p.SupervisionDir())
	if _, err := store.Arm(supervision.Registration{RunID: "run", RepoID: "repo", CWD: "/work", ClaudeTranscriptBound: true, ClaudeTranscriptOffset: 7}); err != nil {
		t.Fatal(err)
	}
	reg, ok, err := store.Claim("/work", "session")
	if err != nil || !ok {
		t.Fatalf("Claim() = (%+v, %v, %v)", reg, ok, err)
	}
	applySupervisorOutcome(store, p, reg, supervisorHookEvent{SessionID: "session", HandoffID: "turn", TranscriptOffset: 11}, supervisorAskUser, &ipc.RunInfo{ID: "run", RepoID: "repo", Status: types.RunRunning}, io.Discard)
	reg, ok, err = store.Get("run")
	if err != nil || !ok {
		t.Fatalf("Get() = (%+v, %v, %v)", reg, ok, err)
	}
	if reg.Phase != supervision.PhaseAwaitingUser || reg.ClaudeTranscriptOffset != 11 {
		t.Fatalf("ask-user registration = %+v, want advanced awaiting-user registration", reg)
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
				applySupervisorOutcome(store, p, reg, supervisorHookEvent{SessionID: "session", HandoffID: "turn-" + string(rune('0'+i))}, supervisorHeartbeat, run, &out)
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
