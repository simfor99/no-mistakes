package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/supervision"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
)

// codexHookEvent is the stable subset of the official Codex command-hook
// payload needed to bind an explicitly armed run to the session that ended.
// Unknown fields remain intentionally ignored for forward compatibility.
type codexHookEvent struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

var (
	superviseResume = resumeCodexSession
	superviseWatch  = runWatchProcess
	superviseSteps  = func(d *db.DB, runID string) ([]*db.StepResult, error) { return d.GetStepsByRun(runID) }
	superviseSpawn  = spawnSupervisorWorker
	superviseNotify = notifySupervisorUser
)

func newAxiSuperviseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "supervise", Short: "Opt-in Codex CLI supervision for one AXI run", SilenceErrors: true, SilenceUsage: true}
	cmd.AddCommand(newAxiSuperviseArmCmd())
	cmd.AddCommand(newAxiSuperviseStatusCmd())
	worker := &cobra.Command{Use: "worker", Hidden: true, Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true}
	var runID string
	worker.Flags().StringVar(&runID, "run", "", "armed run id")
	worker.RunE = func(cmd *cobra.Command, args []string) error {
		return runAxiSuperviseWorker(strings.TrimSpace(runID))
	}
	cmd.AddCommand(worker)
	return cmd
}

func newAxiSuperviseStatusCmd() *cobra.Command {
	var runID string
	cmd := &cobra.Command{Use: "status", Short: "Show the local supervisor state for one run", Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().StringVar(&runID, "run", "", "run id to inspect (required)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runAxiSuperviseStatus(cmd, strings.TrimSpace(runID))
	}
	return cmd
}

func newAxiSuperviseArmCmd() *cobra.Command {
	var runID string
	cmd := &cobra.Command{Use: "arm", Short: "Arm one active run for an installed Codex Stop hook", Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().StringVar(&runID, "run", "", "run id to supervise (required)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runAxiSuperviseArm(cmd, strings.TrimSpace(runID))
	}
	return cmd
}

func newAxiCodexHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "codex-hook",
		Short:         "Codex lifecycle hook adapter for armed supervision",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAxiCodexHook(cmd.InOrStdin())
		},
	}
}

func runAxiSuperviseArm(cmd *cobra.Command, runID string) error {
	if runID == "" {
		return emitError(cmd, 2, "--run is required", "Run `no-mistakes axi supervise arm --run <id>` after the AXI run id is known")
	}
	env, err := openAxiEnv(false)
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	defer env.close()
	run, err := env.d.GetRun(runID)
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("get run: %v", err))
	}
	if run == nil || run.RepoID != env.repo.ID || terminalStatus(string(run.Status)) {
		return emitError(cmd, 1, fmt.Sprintf("run %q is not an active run for this repository", runID))
	}
	cwd, err := supervisorWorktreeRoot()
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	reg, err := supervision.NewStore(env.p.SupervisionDir()).Arm(supervision.Registration{RunID: runID, RepoID: env.repo.ID, CWD: cwd})
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	emitDoc(cmd,
		toonField("supervision", "armed"),
		toonField("run_id", reg.RunID),
		toonField("cwd", reg.CWD),
		toonField("hook_required", true),
		toonField("help", []string{"Install the documented Codex Stop hook before ending this turn; without it, no worker will start."}),
	)
	return nil
}

func runAxiSuperviseStatus(cmd *cobra.Command, runID string) error {
	if runID == "" {
		return emitError(cmd, 2, "--run is required", "Run `no-mistakes axi supervise status --run <id>`")
	}
	p, d, err := openResources()
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	defer d.Close()
	reg, found, err := supervision.NewStore(p.SupervisionDir()).Get(runID)
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	if !found {
		return emitError(cmd, 1, "no local supervision is registered for this run")
	}
	fields := []toon.Field{
		toonField("supervision", reg.Phase),
		toonField("run_id", reg.RunID),
		toonField("session_bound", reg.SessionID != ""),
		toonField("updated_at", reg.UpdatedAt),
	}
	if reg.Error != "" {
		fields = append(fields, toonField("error", reg.Error))
	}
	emitDoc(cmd, fields...)
	return nil
}

func runAxiCodexHook(in io.Reader) error {
	var event codexHookEvent
	if err := json.NewDecoder(io.LimitReader(in, 64<<10)).Decode(&event); err != nil {
		return nil // A global hook must be harmless for non-Codex or malformed input.
	}
	if event.HookEventName != "Stop" {
		return nil
	}
	cwd, err := canonicalSupervisorCWD(event.CWD)
	if err != nil || strings.TrimSpace(event.SessionID) == "" {
		return nil
	}
	p, d, err := openResources()
	if err != nil {
		return nil
	}
	defer d.Close()
	store := supervision.NewStore(p.SupervisionDir())
	reg, found, err := store.FindByCWD(cwd)
	if err != nil || !found {
		return nil
	}
	if reg.Phase == supervision.PhaseArmed {
		reg, found, err = store.Claim(cwd, event.SessionID)
		if err != nil || !found {
			return nil
		}
	} else if reg.SessionID != event.SessionID {
		return nil
	}
	run, err := d.GetRun(reg.RunID)
	if err != nil || run == nil {
		reg.Phase, reg.Error = supervision.PhaseResumeFailed, "registered run is unavailable"
		_ = store.Save(reg)
		return nil
	}
	if terminalStatus(string(run.Status)) {
		reg.Phase = supervision.PhaseCompleted
		_ = store.Save(reg)
		return nil
	}
	if run.AwaitingAgentSince != nil {
		firstUserHandoff := reg.Phase != supervision.PhaseAwaitingUser
		reg.Phase = supervision.PhaseAwaitingUser
		_ = store.Save(reg)
		if firstUserHandoff {
			superviseNotify("No-Mistakes braucht eine Entscheidung", "Der Run wartet auf deine Antwort in Codex.")
		}
		return nil
	}
	reg.Phase = supervision.PhaseWatching
	if err := store.Save(reg); err != nil {
		return nil
	}
	started, err := store.AcquireWorker(reg.RunID)
	if err != nil || !started {
		return nil
	}
	if err := superviseSpawn(p.Root(), reg.CWD, reg.RunID); err != nil {
		_ = store.ReleaseWorker(reg.RunID)
		reg.Phase, reg.Error = supervision.PhaseResumeFailed, "start worker: "+err.Error()
		_ = store.Save(reg)
	}
	return nil
}

func runAxiSuperviseWorker(runID string) error {
	if runID == "" {
		return fmt.Errorf("--run is required")
	}
	p, err := paths.New()
	if err != nil {
		return err
	}
	store := supervision.NewStore(p.SupervisionDir())
	worker, held, err := store.HoldWorker(runID)
	if err != nil || !held {
		return err
	}
	defer worker.Release()
	if err := p.EnsureDirs(); err != nil {
		return recordSupervisorWorkerFailure(store, runID, "open resources: "+err.Error())
	}
	d, err := db.Open(p.DB())
	if err != nil {
		return recordSupervisorWorkerFailure(store, runID, "open resources: "+err.Error())
	}
	defer d.Close()
	reg, found, err := store.Get(runID)
	if err != nil || !found || reg.Phase != supervision.PhaseWatching || reg.SessionID == "" {
		return nil
	}
	if err := superviseWatch(p.Root(), reg.CWD, runID); err != nil {
		run, getErr := d.GetRun(runID)
		if getErr != nil || run == nil || !terminalStatus(string(run.Status)) {
			reg.Phase, reg.Error = supervision.PhaseResumeFailed, "watch: "+err.Error()
			_ = store.Save(reg)
			return nil
		}
	}
	run, err := d.GetRun(runID)
	if err != nil || run == nil {
		reg.Phase, reg.Error = supervision.PhaseResumeFailed, "registered run is unavailable after watch"
		return store.Save(reg)
	}
	if terminalStatus(string(run.Status)) {
		reg.Phase = supervision.PhaseCompleted
		return store.Save(reg)
	}
	steps, err := superviseSteps(d, runID)
	if err != nil {
		return recordSupervisorWorkerFailure(store, runID, "load watched run steps: "+err.Error())
	}
	reg, resume, err := store.Handoff(runID, supervisorFingerprint(run, steps))
	if err != nil {
		return err
	}
	if !resume {
		return nil
	}
	// Release before resume: the resumed turn's Stop hook must be able to start
	// the next watch phase after it sends axi respond or asks Simon a question.
	if err := worker.Release(); err != nil {
		return err
	}
	if err := superviseResume(reg.CWD, reg.SessionID, runID); err != nil {
		reg.Phase, reg.Error = supervision.PhaseResumeFailed, "resume Codex: "+err.Error()
		_ = store.Save(reg)
		superviseNotify("No-Mistakes-Supervisor konnte Codex nicht fortsetzen", "Öffne den AXI-Status für die gespeicherte Diagnose.")
	}
	return nil
}

func runWatchProcess(nmHome, cwd, runID string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open null device: %w", err)
	}
	defer devNull.Close()
	cmd := exec.Command(exe, "axi", "watch", "--run", runID, "--until", "attention")
	cmd.Dir = cwd
	cmd.Env = supervisorEnv(nmHome)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = devNull, devNull, nil
	shellenv.ConfigureShellCommand(cmd)
	if err := shellenv.RunShellCommand(cmd); err != nil {
		return fmt.Errorf("watch process: %w", err)
	}
	return nil
}

func resumeCodexSession(cwd, sessionID, runID string) error {
	path, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("find codex: %w", err)
	}
	prompt := fmt.Sprintf("No-Mistakes supervision event for run %s. Read `no-mistakes axi status --run %s`, continue only the allowed technical AXI action, and keep Simon's CEO-decision boundary. Do not report this wake as completion.", runID, runID)
	cmd := exec.Command(path, "exec", "-C", cwd, "resume", sessionID, prompt)
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	shellenv.ConfigureShellCommand(cmd)
	if err := shellenv.RunShellCommand(cmd); err != nil {
		return err
	}
	return nil
}

func supervisorWorktreeRoot() (string, error) {
	root, err := git.FindGitRoot(".")
	if err != nil {
		return "", fmt.Errorf("find current worktree root: %w", err)
	}
	return canonicalSupervisorCWD(root)
}

func recordSupervisorWorkerFailure(store *supervision.Store, runID, message string) error {
	reg, found, err := store.Get(runID)
	if err == nil && found {
		reg.Phase, reg.Error = supervision.PhaseResumeFailed, message
		_ = store.Save(reg)
	}
	return fmt.Errorf("%s", message)
}

func supervisorFingerprint(run *db.Run, steps []*db.StepResult) string {
	var state strings.Builder
	state.WriteString(string(run.Status))
	state.WriteByte('|')
	state.WriteString(fmt.Sprintf("%d|%t", run.UpdatedAt, run.AwaitingAgentSince != nil))
	for _, step := range steps {
		state.WriteByte('|')
		state.WriteString(string(step.StepName))
		state.WriteByte(':')
		state.WriteString(string(step.Status))
		if step.LastActivityAt != nil {
			state.WriteString(fmt.Sprintf(":%d", *step.LastActivityAt))
		}
	}
	sum := sha256.Sum256([]byte(state.String()))
	return fmt.Sprintf("%x", sum[:])
}

func canonicalSupervisorCWD(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		var err error
		value, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get cwd: %w", err)
		}
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	return filepath.Clean(abs), nil
}

func notifySupervisorUser(title, message string) {
	if path, err := exec.LookPath("notify-send"); err == nil {
		cmd := exec.Command(path, title, message)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = io.Discard, io.Discard, nil
		_ = cmd.Start()
	}
}

func supervisorEnv(nmHome string) []string {
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "NM_HOME=") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "NM_HOME="+nmHome)
}

// toonField avoids exporting the TOON dependency into this package's command
// plumbing while keeping arm output consistent with the rest of AXI.
func toonField(key string, value any) toon.Field { return toon.Field{Key: key, Value: value} }
