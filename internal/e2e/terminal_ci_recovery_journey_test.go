//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// installStatefulGHStub replaces the harness fakeagent gh symlink with a
// shell stub whose PR state is re-read from a file on every invocation, so a
// test can move the PR to a terminal state while the daemon is down. It
// returns the state file path.
func installStatefulGHStub(t *testing.T, h *Harness) string {
	t.Helper()
	statePath := filepath.Join(filepath.Dir(h.AgentLog), "pr-state.txt")
	ghPath := filepath.Join(h.BinDir, "gh")
	if err := os.Remove(ghPath); err != nil {
		t.Fatalf("remove fakeagent gh symlink: %v", err)
	}
	script := `#!/bin/sh
set -u
state_file='` + statePath + `'
if [ "$#" -ge 2 ] && [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$#" -ge 2 ] && [ "$1" = "pr" ]; then
  case "$2" in
    list) echo "[]"; exit 0 ;;
    create) echo "https://github.com/e2e-owner/no-mistakes/pull/7"; exit 0 ;;
    checks) echo "[]"; exit 0 ;;
    view)
      case " $* " in
        *" --json state"*) cat "$state_file" 2>/dev/null || echo "OPEN"; exit 0 ;;
        *" --json mergeable"*) echo "MERGEABLE"; exit 0 ;;
      esac
      ;;
  esac
fi
if [ "$#" -ge 2 ] && [ "$1" = "api" ]; then
  case "$*" in
    *required_status_checks*) echo '{"contexts":[],"checks":[]}'; exit 0 ;;
    *check-runs*) echo '{"total_count":0,"check_runs":[]}'; exit 0 ;;
    *statuses*) echo '[]'; exit 0 ;;
    *rules/branches*) echo '[]'; exit 0 ;;
  esac
fi
echo "stateful gh stub: unsupported arguments: $*" >&2
exit 1
`
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stateful gh stub: %v", err)
	}
	return statePath
}

// killDaemonHard SIGKILLs the daemon owning the harness NM_HOME and waits for
// the process to disappear. This emulates the daemon crashing without any
// cleanup: the run row stays running in the database and the singleton lock
// is released only by the kernel. It returns the killed PID.
func killDaemonHard(t *testing.T, h *Harness) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.NMHome, "daemon.pid"))
	if err != nil {
		t.Fatalf("read daemon pid file: %v", err)
	}
	var record struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("parse daemon pid file: %v", err)
	}
	if record.PID <= 1 {
		t.Fatalf("refusing to kill daemon pid %d", record.PID)
	}
	if out, err := exec.Command("kill", "-9", strconv.Itoa(record.PID)).CombinedOutput(); err != nil {
		t.Fatalf("SIGKILL daemon pid %d: %v\n%s", record.PID, err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", strconv.Itoa(record.PID)).Run(); err != nil {
			return record.PID
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon pid %d still alive after SIGKILL", record.PID)
	return 0
}

// waitForCIStepRunning polls the daemon until the newest run for branch has
// its CI step in the running state, i.e. the run is an active CI monitor.
func waitForCIStepRunning(t *testing.T, h *Harness, branch string, timeout time.Duration) (runID string, prURL string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	logged := false
	for time.Now().Before(deadline) {
		runs := h.Runs()
		for i := range runs {
			r := &runs[i]
			if r.Branch != branch {
				continue
			}
			if r.PRURL != nil {
				prURL = *r.PRURL
			}
			if !logged {
				logged = true
				var steps []string
				for _, step := range r.Steps {
					steps = append(steps, fmt.Sprintf("%s=%s", step.StepName, step.Status))
				}
				t.Logf("first sight of run %s: status=%s error=%v steps=[%s]", r.ID, r.Status, deref(r.Error), strings.Join(steps, " "))
			}
			for _, step := range r.Steps {
				if step.StepName == types.StepCI && step.Status == types.StepStatusRunning {
					return r.ID, prURL
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	h.dumpDebugState()
	t.Fatalf("CI step for branch %s never reached the running state within %v", branch, timeout)
	return "", ""
}

// readRunRow opens the gate database and returns the persisted run row, so
// assertions can cover database-only custody columns no IPC surface exposes.
func readRunRow(t *testing.T, h *Harness, runID string) *db.Run {
	t.Helper()
	p := paths.WithRoot(h.NMHome)
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open gate database: %v", err)
	}
	defer database.Close()
	run, err := database.GetRun(runID)
	if err != nil {
		t.Fatalf("read run %s: %v", runID, err)
	}
	if run == nil {
		t.Fatalf("run %s disappeared from the gate database", runID)
	}
	return run
}

// exportJourneyEvidence writes a product-level transcript artifact when
// NM_E2E_EVIDENCE_DIR is set. Subtest names contain slashes, so each artifact
// may land in its own subdirectory. It is a no-op in ordinary CI runs.
func exportJourneyEvidence(t *testing.T, name, content string) {
	t.Helper()
	dir := os.Getenv("NM_E2E_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Logf("evidence dir: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Logf("write evidence %s: %v", path, err)
		return
	}
	t.Logf("evidence: %s", path)
}

// TestTerminalCIRecoveryJourney reproduces the operator journey the terminal
// CI recovery fix ships: a pipeline run parks in the CI monitor watching an
// open PR, the daemon crashes, and the PR reaches a terminal state while no
// daemon is running. On the next daemon start the run must be completed from
// the authoritative remote PR state with push custody cleared, while a PR
// that is still open keeps the ordinary fail-closed crash recovery.
func TestTerminalCIRecoveryJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stateful sh gh stub and POSIX kill are unavailable on windows")
	}
	for _, tc := range []struct {
		name             string
		prStateOnRestart string
		wantRun          types.RunStatus
		wantStep         types.StepStatus
	}{
		{name: "merged PR recovers run as completed", prStateOnRestart: "MERGED", wantRun: types.RunCompleted, wantStep: types.StepStatusCompleted},
		{name: "open PR stays fail closed", prStateOnRestart: "OPEN", wantRun: types.RunFailed, wantStep: types.StepStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := NewHarness(t, SetupOpts{Agent: "claude"})

			// The fork-routed init stores the literal github parent URL in the
			// gate repo row (a plain init would store the insteadOf-resolved
			// file:// URL, which makes the PR and CI steps skip as an
			// unsupported provider), while pushes still land in local bare
			// repositories through the URL rewrites.
			upstreamURL := "https://github.com/e2e-owner/no-mistakes.git"
			forkURL := "https://github.com/e2e-fork/no-mistakes.git"
			forkDir := filepath.Join(filepath.Dir(h.UpstreamDir), "fork.git")
			if err := os.MkdirAll(forkDir, 0o755); err != nil {
				t.Fatalf("mkdir fork: %v", err)
			}
			if out, err := h.runGit(ctx, forkDir, "init", "--bare", "--initial-branch=main"); err != nil {
				t.Fatalf("init fork: %v\n%s", err, out)
			}
			if out, err := h.runGit(ctx, h.WorkDir, "push", forkDir, "main"); err != nil {
				t.Fatalf("seed fork main: %v\n%s", err, out)
			}
			configureGitURLRewrite(t, h, upstreamURL, h.UpstreamDir)
			configureGitURLRewrite(t, h, forkURL, forkDir)
			if out, err := h.runGit(ctx, h.WorkDir, "remote", "set-url", "origin", upstreamURL); err != nil {
				t.Fatalf("set origin to github URL: %v\n%s", err, out)
			}
			prStateFile := installStatefulGHStub(t, h)
			if err := os.WriteFile(prStateFile, []byte("OPEN\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if out, err := h.Run("init", "--fork-url", forkURL); err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}

			branch := "feature/terminal-ci-recovery"
			h.CommitChange(branch, "ci-recovery.txt", "terminal CI recovery probe\n", "add terminal CI recovery probe")
			h.PushToGate(branch)

			runID, prURL := waitForCIStepRunning(t, h, branch, 150*time.Second)
			if !strings.Contains(prURL, "/pull/") {
				t.Fatalf("monitored run has no PR URL: %q", prURL)
			}
			monitoringStatus, statusErr := h.Run("axi", "status")
			t.Logf("run %s monitoring PR %s", runID, prURL)

			killedPID := killDaemonHard(t, h)
			if err := os.WriteFile(prStateFile, []byte(tc.prStateOnRestart+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			// The operator brings the daemon back the documented way after a
			// crash; startup recovery runs before the socket starts serving.
			startOut, err := h.Run("daemon", "start")
			if err != nil {
				t.Fatalf("daemon start after crash: %v\n%s", err, startOut)
			}

			final := h.WaitForRun(branch, 150*time.Second)
			if final.Status != tc.wantRun {
				t.Fatalf("run status after restart = %s (error %v), want %s", final.Status, deref(final.Error), tc.wantRun)
			}
			var ciStatus types.StepStatus
			for _, step := range final.Steps {
				if step.StepName == types.StepCI {
					ciStatus = step.Status
				}
			}
			if ciStatus != tc.wantStep {
				t.Fatalf("CI step status after restart = %s, want %s", ciStatus, tc.wantStep)
			}

			row := readRunRow(t, h, runID)
			if tc.wantRun == types.RunCompleted {
				if row.Error != nil {
					t.Fatalf("recovered run retained error: %q", *row.Error)
				}
				if row.PRState == nil || *row.PRState != "merged" {
					t.Fatalf("recovered run PR state = %v, want merged", row.PRState)
				}
			} else if row.Error == nil {
				t.Fatal("fail-closed run lost its error message")
			}
			if row.PushActive {
				t.Fatal("run retained active push custody after recovery")
			}
			if row.AwaitingAgentSince != nil {
				t.Fatal("run retained awaiting-agent marker after recovery")
			}
			worktreeDir := paths.WithRoot(h.NMHome).WorktreeDir(h.repoID(), runID)
			if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
				t.Fatalf("terminal run worktree %s still present: %v", worktreeDir, err)
			}

			// Path-level proof: the merged run must have been completed by the
			// terminal CI recovery itself, and the open run must have gone
			// through the ordinary fail-closed stale-run recovery instead.
			daemonLog := readDaemonLog(t, h)
			const recoveryLine = "completed CI run from terminal PR state after daemon restart"
			const staleLine = "recovered stale runs from previous crash"
			if tc.wantRun == types.RunCompleted {
				if !strings.Contains(daemonLog, recoveryLine) {
					t.Fatalf("daemon log lacks the terminal CI recovery line %q:\n%s", recoveryLine, tailForLog(daemonLog))
				}
				if strings.Contains(daemonLog, staleLine) {
					t.Fatalf("recovered run also went through fail-closed stale recovery:\n%s", tailForLog(daemonLog))
				}
			} else {
				if strings.Contains(daemonLog, recoveryLine) {
					t.Fatalf("open-PR run was completed by terminal CI recovery:\n%s", tailForLog(daemonLog))
				}
				if !strings.Contains(daemonLog, staleLine) {
					t.Fatalf("daemon log lacks fail-closed stale run recovery %q:\n%s", staleLine, tailForLog(daemonLog))
				}
			}

			runsOut, runsErr := h.Run("runs")
			exportJourneyEvidence(t, fmt.Sprintf("%s-axi-status-while-monitoring.txt", t.Name()), withHeader(
				fmt.Sprintf("$ no-mistakes axi status (daemon alive, run %s parked in CI monitor for %s)", runID, prURL),
				monitoringStatus, statusErr))
			exportJourneyEvidence(t, fmt.Sprintf("%s-crash-and-flip.txt", t.Name()), withHeader(
				fmt.Sprintf("$ kill -9 %d (daemon crash; run %s left 'running' in the gate DB)", killedPID, runID),
				fmt.Sprintf("$ echo %s > pr-state   # PR reaches this state while no daemon is running", tc.prStateOnRestart), nil))
			exportJourneyEvidence(t, fmt.Sprintf("%s-daemon-start.txt", t.Name()), withHeader(
				"$ no-mistakes daemon start   # startup recovery runs before the socket serves", startOut, nil))
			exportJourneyEvidence(t, fmt.Sprintf("%s-runs-after-recovery.txt", t.Name()), withHeader(
				"$ no-mistakes runs", runsOut, runsErr))
			exportJourneyEvidence(t, fmt.Sprintf("%s-db-run-row.json", t.Name()), withHeader(
				fmt.Sprintf("gate database run row for %s (custody columns included)", runID), marshalRunRow(t, row), nil))
			exportJourneyEvidence(t, fmt.Sprintf("%s-daemon-recovery-log.txt", t.Name()), withHeader(
				"daemon log lines covering the restart recovery", filterRecoveryLog(daemonLog), nil))
		})
	}
}

func readDaemonLog(t *testing.T, h *Harness) string {
	t.Helper()
	for _, candidate := range []string{
		filepath.Join(h.NMHome, "logs", "daemon.log"),
		filepath.Join(h.NMHome, "daemon.log"),
	} {
		data, err := os.ReadFile(candidate)
		if err != nil || len(data) == 0 {
			continue
		}
		return string(data)
	}
	return ""
}

// filterRecoveryLog keeps the daemon log lines that tell the restart recovery
// story, so the exported evidence artifact stays readable despite debug-level
// IPC request logging.
func filterRecoveryLog(daemonLog string) string {
	var kept []string
	for _, line := range strings.Split(daemonLog, "\n") {
		if strings.Contains(line, "recovery") || strings.Contains(line, "recovered") ||
			strings.Contains(line, "terminal CI") || strings.Contains(line, "terminal PR state") ||
			strings.Contains(line, "worktree") || strings.Contains(line, "daemon listening") ||
			strings.Contains(line, "singleton") || strings.Contains(line, "crashed during execution") {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return "(no recovery-relevant daemon log lines found)"
	}
	return strings.Join(kept, "\n")
}

// tailForLog bounds failure output to the last part of the daemon log.
func tailForLog(daemonLog string) string {
	lines := strings.Split(strings.TrimRight(daemonLog, "\n"), "\n")
	if len(lines) > 120 {
		lines = lines[len(lines)-120:]
	}
	return strings.Join(lines, "\n")
}

func marshalRunRow(t *testing.T, row *db.Run) string {
	t.Helper()
	payload := struct {
		ID                 string  `json:"id"`
		Branch             string  `json:"branch"`
		Status             string  `json:"status"`
		PRURL              *string `json:"pr_url"`
		PRState            *string `json:"pr_state"`
		PushActive         bool    `json:"push_active"`
		AwaitingAgentSince *int64  `json:"awaiting_agent_since"`
		Error              *string `json:"error"`
	}{
		ID:                 row.ID,
		Branch:             row.Branch,
		Status:             string(row.Status),
		PRURL:              row.PRURL,
		PRState:            row.PRState,
		PushActive:         row.PushActive,
		AwaitingAgentSince: row.AwaitingAgentSince,
		Error:              row.Error,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal run row: %v", err)
	}
	return string(data)
}

func withHeader(header, output string, err error) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	if err != nil {
		fmt.Fprintf(&b, "(command error: %v)\n", err)
	}
	b.WriteString(strings.TrimSpace(output))
	b.WriteString("\n")
	return b.String()
}
