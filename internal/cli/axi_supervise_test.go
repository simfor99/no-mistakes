package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/supervision"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
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
	if err := runAxiCodexHook(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/tmp"}`)); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestCodexHookIgnoresMalformedPayload(t *testing.T) {
	if err := runAxiCodexHook(strings.NewReader(`not json`)); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestCodexHookParksAlreadyAwaitingRunWithoutResuming(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo, err := database.InsertRepoWithID("repo-1", t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	dbRun, err := database.InsertRun(repo.ID, "feature/parked", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(dbRun.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	if err := database.SetRunAwaitingAgent(dbRun.ID); err != nil {
		t.Fatalf("park run: %v", err)
	}
	cwd := t.TempDir()
	store := supervision.NewStore(p.SupervisionDir())
	if _, err := store.Arm(supervision.Registration{RunID: dbRun.ID, RepoID: repo.ID, CWD: cwd}); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}

	previousSpawn, previousNotify := superviseSpawn, superviseNotify
	t.Cleanup(func() {
		superviseSpawn = previousSpawn
		superviseNotify = previousNotify
	})
	spawned, notified := 0, 0
	superviseSpawn = func(string, string, string) error {
		spawned++
		return nil
	}
	superviseNotify = func(string, string) { notified++ }
	event := `{"hook_event_name":"Stop","session_id":"session-1","cwd":"` + cwd + `"}`
	if err := runAxiCodexHook(strings.NewReader(event)); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
	reg, found, err := store.Get(dbRun.ID)
	if err != nil || !found || reg.Phase != supervision.PhaseAwaitingUser || reg.SessionID != "session-1" {
		t.Fatalf("Get() after parked stop = (%+v, %v, %v), want awaiting bound session", reg, found, err)
	}
	if spawned != 0 || notified != 1 {
		t.Fatalf("parked stop spawned=%d notified=%d, want 0 and 1", spawned, notified)
	}
	if _, err := os.Stat(filepath.Join(p.SupervisionDir(), dbRun.ID+".worker.lock")); !os.IsNotExist(err) {
		t.Fatalf("parked stop created worker marker: %v", err)
	}

	if err := database.ClearRunAwaitingAgent(dbRun.ID); err != nil {
		t.Fatalf("clear parked run: %v", err)
	}
	if err := runAxiCodexHook(strings.NewReader(event)); err != nil {
		t.Fatalf("runAxiCodexHook() after response error = %v", err)
	}
	if spawned != 1 {
		t.Fatalf("later stop spawned=%d, want 1", spawned)
	}
	reg, found, err = store.Get(dbRun.ID)
	if err != nil || !found || reg.Phase != supervision.PhaseWatching {
		t.Fatalf("Get() after response = (%+v, %v, %v), want watching", reg, found, err)
	}
	if err := store.ReleaseWorker(dbRun.ID); err != nil {
		t.Fatalf("release fake worker claim: %v", err)
	}
}

func TestSupervisorEnvReplacesExistingNMHome(t *testing.T) {
	t.Setenv("NM_HOME", "/old")
	env := supervisorEnv("/new")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "NM_HOME=") {
			count++
			if entry != "NM_HOME=/new" {
				t.Fatalf("NM_HOME entry = %q, want replacement", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("NM_HOME count = %d, want 1", count)
	}
}

func TestAxiSuperviseArmBindsLinkedWorktree(t *testing.T) {
	main := setupTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	run(t, main, "git", "worktree", "add", "-b", "feature/linked", linked)
	linkedRoot, err := filepath.EvalSymlinks(linked)
	if err != nil {
		linkedRoot = linked
	}
	mainRoot, err := filepath.EvalSymlinks(main)
	if err != nil {
		mainRoot = main
	}
	chdir(t, linkedRoot)

	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New() error = %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo, err := database.InsertRepoWithID("repo-1", mainRoot, "origin", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	dbRun, err := database.InsertRun(repo.ID, "feature/linked", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(dbRun.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(new(bytes.Buffer))
	if err := runAxiSuperviseArm(cmd, dbRun.ID); err != nil {
		t.Fatalf("runAxiSuperviseArm() error = %v", err)
	}
	reg, found, err := supervision.NewStore(p.SupervisionDir()).Get(dbRun.ID)
	if err != nil || !found {
		t.Fatalf("Get() = (%+v, %v, %v), want registration", reg, found, err)
	}
	if reg.CWD != linkedRoot {
		t.Fatalf("armed cwd = %q, want linked worktree %q", reg.CWD, linkedRoot)
	}
}

func TestAxiSuperviseCommandsPersistOnlyLocalRegistration(t *testing.T) {
	repoDir := setupTestRepo(t)
	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New() error = %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo, err := database.InsertRepoWithID("repo-supervise", repoDir, "origin", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	run, err := database.InsertRun(repo.ID, "feature/supervise", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}

	armed, err := executeCmd("axi", "supervise", "arm", "--run", run.ID)
	if err != nil {
		t.Fatalf("axi supervise arm error = %v", err)
	}
	for _, want := range []string{
		"supervision: armed",
		"run_id: \"" + run.ID + "\"",
		"hook_required: true",
		"without it, no worker will start",
	} {
		if !strings.Contains(armed, want) {
			t.Errorf("axi supervise arm output missing %q in:\n%s", want, armed)
		}
	}

	status, err := executeCmd("axi", "supervise", "status", "--run", run.ID)
	if err != nil {
		t.Fatalf("axi supervise status error = %v", err)
	}
	for _, want := range []string{"supervision: armed", "session_bound: false"} {
		if !strings.Contains(status, want) {
			t.Errorf("axi supervise status output missing %q in:\n%s", want, status)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("axi supervise arm wrote a global Codex hook: %v", err)
	}
	t.Logf("end-user axi supervise transcript:\n%s%s", armed, status)
}

func TestAxiSuperviseWorkerReleasesClaimAfterResourceFailure(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	store := supervision.NewStore(p.SupervisionDir())
	reg, err := store.Arm(supervision.Registration{RunID: "run-1", RepoID: "repo-1", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	reg, claimed, err := store.Claim(reg.CWD, "session-1")
	if err != nil || !claimed {
		t.Fatalf("Claim() = (%+v, %v, %v), want claimed registration", reg, claimed, err)
	}
	if got, err := store.AcquireWorker(reg.RunID); err != nil || !got {
		t.Fatalf("AcquireWorker() = (%v, %v), want (true, nil)", got, err)
	}
	if err := os.Mkdir(p.DB(), 0o755); err != nil {
		t.Fatalf("create database blocker: %v", err)
	}
	if err := runAxiSuperviseWorker(reg.RunID); err == nil {
		t.Fatal("runAxiSuperviseWorker() error = nil, want resource failure")
	}
	if got, err := store.AcquireWorker(reg.RunID); err != nil || !got {
		t.Fatalf("AcquireWorker() after resource failure = (%v, %v), want released lock", got, err)
	}
	updated, found, err := store.Get(reg.RunID)
	if err != nil || !found || updated.Phase != supervision.PhaseResumeFailed {
		t.Fatalf("Get() after resource failure = (%+v, %v, %v), want visible failure", updated, found, err)
	}
}

func TestAxiSuperviseWorkerDoesNotResumeUnchangedEventTwice(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo, err := database.InsertRepoWithID("repo-1", t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	dbRun, err := database.InsertRun(repo.ID, "feature/supervise", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(dbRun.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	store := supervision.NewStore(p.SupervisionDir())
	reg, err := store.Arm(supervision.Registration{RunID: dbRun.ID, RepoID: repo.ID, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	reg, claimed, err := store.Claim(reg.CWD, "session-1")
	if err != nil || !claimed {
		t.Fatalf("Claim() = (%+v, %v, %v), want claimed registration", reg, claimed, err)
	}

	previousWatch, previousResume := superviseWatch, superviseResume
	t.Cleanup(func() {
		superviseWatch = previousWatch
		superviseResume = previousResume
	})
	superviseWatch = func(string, string, string) error { return nil }
	resumes := 0
	superviseResume = func(string, string, string) error {
		resumes++
		return nil
	}

	if got, err := store.AcquireWorker(dbRun.ID); err != nil || !got {
		t.Fatalf("AcquireWorker() = (%v, %v), want (true, nil)", got, err)
	}
	if err := runAxiSuperviseWorker(dbRun.ID); err != nil {
		t.Fatalf("first runAxiSuperviseWorker() error = %v", err)
	}
	reg, found, err := store.Get(dbRun.ID)
	if err != nil || !found {
		t.Fatalf("Get() after first worker = (%+v, %v, %v), want registration", reg, found, err)
	}
	reg.Phase = supervision.PhaseWatching
	if err := store.Save(reg); err != nil {
		t.Fatalf("restore watching state: %v", err)
	}
	if got, err := store.AcquireWorker(dbRun.ID); err != nil || !got {
		t.Fatalf("AcquireWorker() for unchanged event = (%v, %v), want (true, nil)", got, err)
	}
	if err := runAxiSuperviseWorker(dbRun.ID); err != nil {
		t.Fatalf("second runAxiSuperviseWorker() error = %v", err)
	}
	if resumes != 1 {
		t.Fatalf("resume count = %d, want 1 for an unchanged event", resumes)
	}
	reg, found, err = store.Get(dbRun.ID)
	if err != nil || !found || reg.Phase != supervision.PhaseAwaitingUser {
		t.Fatalf("Get() after unchanged event = (%+v, %v, %v), want awaiting user", reg, found, err)
	}
}

func TestAxiSuperviseWorkerRecordsStepReadFailure(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	repo, err := database.InsertRepoWithID("repo-1", t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	dbRun, err := database.InsertRun(repo.ID, "feature/supervise", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := database.UpdateRunStatus(dbRun.ID, types.RunRunning); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	store := supervision.NewStore(p.SupervisionDir())
	reg, err := store.Arm(supervision.Registration{RunID: dbRun.ID, RepoID: repo.ID, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	reg, claimed, err := store.Claim(reg.CWD, "session-1")
	if err != nil || !claimed {
		t.Fatalf("Claim() = (%+v, %v, %v), want claimed registration", reg, claimed, err)
	}

	previousWatch, previousSteps := superviseWatch, superviseSteps
	t.Cleanup(func() {
		superviseWatch = previousWatch
		superviseSteps = previousSteps
	})
	superviseWatch = func(string, string, string) error { return nil }
	superviseSteps = func(*db.DB, string) ([]*db.StepResult, error) { return nil, errors.New("step read unavailable") }

	if got, err := store.AcquireWorker(dbRun.ID); err != nil || !got {
		t.Fatalf("AcquireWorker() = (%v, %v), want (true, nil)", got, err)
	}
	if err := runAxiSuperviseWorker(dbRun.ID); err == nil {
		t.Fatal("runAxiSuperviseWorker() error = nil, want step read failure")
	}
	updated, found, err := store.Get(dbRun.ID)
	if err != nil || !found || updated.Phase != supervision.PhaseResumeFailed || !strings.Contains(updated.Error, "step read unavailable") {
		t.Fatalf("Get() after step read failure = (%+v, %v, %v), want visible failure", updated, found, err)
	}
}
