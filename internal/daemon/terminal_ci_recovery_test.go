package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	gitpkg "github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestRecoverOnStartupReconcilesOnlyTerminalActiveCI(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prState   string
		setPRURL  bool
		wantRun   types.RunStatus
		wantStep  types.StepStatus
	}{
		{name: "merged PR completes", prState: "MERGED", setPRURL: true, wantRun: types.RunCompleted, wantStep: types.StepStatusCompleted},
		{name: "open PR fails closed", prState: "OPEN", setPRURL: true, wantRun: types.RunFailed, wantStep: types.StepStatusFailed},
		{name: "run without PR fails closed even when PR state is terminal", prState: "MERGED", wantRun: types.RunFailed, wantStep: types.StepStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := paths.WithRoot(root)
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			database, err := db.Open(p.DB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })

			repo, headSHA := setupTestGitRepo(t, p, database, "terminal-ci")
			run, err := database.InsertRun(repo.ID, "feature/terminal-ci", headSHA, headSHA)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
				t.Fatal(err)
			}
			if tc.setPRURL {
				if err := database.UpdateRunPRURL(run.ID, "https://github.com/test/repo/pull/42"); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.SetRunPushActive(run.ID, true); err != nil {
				t.Fatal(err)
			}
			workDir := p.WorktreeDir(repo.ID, run.ID)
			if err := gitpkg.WorktreeAdd(context.Background(), p.RepoDir(repo.ID), workDir, headSHA); err != nil {
				t.Fatal(err)
			}
			for _, stepName := range types.AllSteps()[:len(types.AllSteps())-1] {
				stepResult, err := database.InsertStepResult(run.ID, stepName)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.CompleteStep(stepResult.ID, 0, 1, filepath.Join(root, string(stepName)+".log")); err != nil {
					t.Fatal(err)
				}
			}
			ciStep, err := database.InsertStepResult(run.ID, types.StepCI)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.StartStep(ciStep.ID); err != nil {
				t.Fatal(err)
			}

			fakeBin, _ := writeMockGHState(t, t.TempDir(), tc.prState)
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

			recoverOnStartup(database, p, NewRunManager(database, p, nil))

			persistedRun, err := database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			persistedStep, err := database.GetStepResult(ciStep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persistedRun.Status != tc.wantRun || persistedStep.Status != tc.wantStep {
				t.Fatalf("recovery result: run=%s step=%s, want run=%s step=%s", persistedRun.Status, persistedStep.Status, tc.wantRun, tc.wantStep)
			}
			if tc.wantRun == types.RunCompleted && persistedRun.Error != nil {
				t.Fatalf("completed run retained error: %q", *persistedRun.Error)
			}
			if tc.wantRun == types.RunCompleted && persistedRun.PushActive {
				t.Fatal("completed run retained active push custody")
			}
			if _, err := os.Stat(filepath.Join(workDir, ".git")); !os.IsNotExist(err) {
				t.Fatalf("terminal worktree was not cleaned up: %v", err)
			}
		})
	}
}
