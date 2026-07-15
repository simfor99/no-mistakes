// Package supervision persists the smallest possible amount of local state
// needed to reconnect an explicitly armed No-Mistakes run to a Codex Stop hook.
package supervision

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Phase string

const (
	PhaseArmed             Phase = "armed"
	PhaseWatching          Phase = "watching"
	PhaseHandoffInProgress Phase = "handoff_in_progress"
	PhaseAwaitingUser      Phase = "awaiting_user"
	PhaseCompleted         Phase = "completed"
	PhaseResumeFailed      Phase = "resume_failed"
)

type Registration struct {
	RunID       string `json:"run_id"`
	RepoID      string `json:"repo_id"`
	CWD         string `json:"cwd"`
	SessionID   string `json:"session_id,omitempty"`
	Phase       Phase  `json:"phase"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Path(runID string) string { return filepath.Join(s.dir, runID+".json") }

func (s *Store) Arm(reg Registration) (Registration, error) {
	if strings.TrimSpace(reg.RunID) == "" || strings.TrimSpace(reg.RepoID) == "" || strings.TrimSpace(reg.CWD) == "" {
		return Registration{}, fmt.Errorf("run id, repo id, and cwd are required")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Registration{}, fmt.Errorf("create supervision directory: %w", err)
	}
	unlock, err := s.acquireClaimLock()
	if err != nil {
		return Registration{}, err
	}
	defer unlock()
	regs, err := s.all()
	if err != nil {
		return Registration{}, err
	}
	for _, existing := range regs {
		if existing.CWD != reg.CWD || existing.Phase == PhaseCompleted || existing.Phase == PhaseResumeFailed {
			continue
		}
		return Registration{}, fmt.Errorf("supervision is already active for this working directory")
	}
	reg.Phase = PhaseArmed
	reg.SessionID = ""
	reg.Fingerprint = ""
	reg.Error = ""
	reg.UpdatedAt = time.Now().UTC().Unix()
	if err := s.write(reg); err != nil {
		return Registration{}, err
	}
	return reg, nil
}

// Claim binds one armed registration for cwd to a session id. Returning false
// is an ordinary no-op: hooks run for every Codex turn, not just supervision.
func (s *Store) Claim(cwd, sessionID string) (Registration, bool, error) {
	if strings.TrimSpace(cwd) == "" || strings.TrimSpace(sessionID) == "" {
		return Registration{}, false, nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Registration{}, false, fmt.Errorf("create supervision directory: %w", err)
	}
	unlock, err := s.acquireClaimLock()
	if os.IsExist(err) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	defer unlock()
	regs, err := s.all()
	if err != nil {
		return Registration{}, false, err
	}
	for _, reg := range regs {
		if reg.CWD != cwd || reg.Phase != PhaseArmed {
			continue
		}
		reg.SessionID = sessionID
		reg.Phase = PhaseWatching
		reg.UpdatedAt = time.Now().UTC().Unix()
		if err := s.write(reg); err != nil {
			return Registration{}, false, err
		}
		return reg, true, nil
	}
	return Registration{}, false, nil
}

func (s *Store) Get(runID string) (Registration, bool, error) {
	data, err := os.ReadFile(s.Path(runID))
	if os.IsNotExist(err) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, fmt.Errorf("read registration: %w", err)
	}
	var reg Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return Registration{}, false, fmt.Errorf("decode registration: %w", err)
	}
	return reg, true, nil
}

// FindByCWD returns the one registration owned by a Codex session in cwd.
// Armed registrations are intentionally included: a Stop hook is what binds an
// otherwise session-less arm request to the session that actually ended.
func (s *Store) FindByCWD(cwd string) (Registration, bool, error) {
	regs, err := s.all()
	if err != nil {
		return Registration{}, false, err
	}
	for _, reg := range regs {
		if reg.CWD == cwd && reg.Phase != PhaseCompleted && reg.Phase != PhaseResumeFailed {
			return reg, true, nil
		}
	}
	return Registration{}, false, nil
}

func (s *Store) acquireClaimLock() (func(), error) {
	lock, err := os.OpenFile(filepath.Join(s.dir, ".claim.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lock.Name())
		return nil, fmt.Errorf("close registration lock: %w", err)
	}
	return func() { _ = os.Remove(lock.Name()) }, nil
}

// AcquireWorker grants one process ownership of a registration's watch phase.
// The lock is intentionally filesystem-backed so separate hook processes
// cannot create duplicate resume turns.
func (s *Store) AcquireWorker(runID string) (bool, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return false, fmt.Errorf("create supervision directory: %w", err)
	}
	file, err := os.OpenFile(s.workerLockPath(runID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock worker: %w", err)
	}
	return true, file.Close()
}

func (s *Store) ReleaseWorker(runID string) error {
	err := os.Remove(s.workerLockPath(runID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) Save(reg Registration) error {
	if strings.TrimSpace(reg.RunID) == "" {
		return fmt.Errorf("run id is required")
	}
	reg.UpdatedAt = time.Now().UTC().Unix()
	return s.write(reg)
}

func (s *Store) all() ([]Registration, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list registrations: %w", err)
	}
	regs := make([]Registration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		reg, ok, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if ok {
			regs = append(regs, reg)
		}
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].RunID < regs[j].RunID })
	return regs, nil
}

func (s *Store) write(reg Registration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("encode registration: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create supervision directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".registration-*")
	if err != nil {
		return fmt.Errorf("create registration temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write registration: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect registration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close registration: %w", err)
	}
	if err := os.Rename(tmpName, s.Path(reg.RunID)); err != nil {
		return fmt.Errorf("replace registration: %w", err)
	}
	return nil
}

func (s *Store) workerLockPath(runID string) string {
	return filepath.Join(s.dir, runID+".worker.lock")
}
