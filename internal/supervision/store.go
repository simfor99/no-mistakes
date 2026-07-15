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

const workerStartGrace = 30 * time.Second

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

type workerLockRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type WorkerLock struct {
	store *Store
	file  *os.File
	runID string
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Path(runID string) string { return filepath.Join(s.dir, runID+".json") }

func (s *Store) Arm(reg Registration) (Registration, error) {
	if strings.TrimSpace(reg.RunID) == "" || strings.TrimSpace(reg.RepoID) == "" || strings.TrimSpace(reg.CWD) == "" {
		return Registration{}, fmt.Errorf("run id, repo id, and cwd are required")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Registration{}, fmt.Errorf("create supervision directory: %w", err)
	}
	lock, held, err := s.acquireClaimLock()
	if err != nil {
		return Registration{}, err
	}
	if !held {
		return Registration{}, fmt.Errorf("supervision registration is busy")
	}
	defer lock.Release()
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
	lock, held, err := s.acquireClaimLock()
	if err != nil {
		return Registration{}, false, err
	}
	if !held {
		return Registration{}, false, nil
	}
	defer lock.Release()
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

func (s *Store) acquireClaimLock() (*storeLock, bool, error) {
	return acquireStoreLock(filepath.Join(s.dir, ".claim.lock"))
}

func (s *Store) AcquireWorker(runID string) (bool, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return false, fmt.Errorf("create supervision directory: %w", err)
	}
	lock, held, err := s.acquireClaimLock()
	if err != nil {
		return false, err
	}
	if !held {
		return false, nil
	}
	defer lock.Release()

	path := s.workerLockPath(runID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s.createWorkerLock(path)
	} else if err != nil {
		return false, fmt.Errorf("inspect worker lock: %w", err)
	}

	worker, active, err := s.tryWorkerLock(runID)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	_ = unlockStoreFile(worker)
	_ = worker.Close()
	if !s.workerLockExpired(path) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove stale worker lock: %w", err)
	}
	return s.createWorkerLock(path)
}

func (s *Store) ReleaseWorker(runID string) error {
	return s.releaseWorker(runID, nil)
}

func (s *Store) HoldWorker(runID string) (*WorkerLock, bool, error) {
	file, active, err := s.tryWorkerLock(runID)
	if err != nil || !active {
		return nil, active, err
	}
	lock := &WorkerLock{store: s, file: file, runID: runID}
	record, err := json.Marshal(workerLockRecord{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		_ = lock.Release()
		return nil, false, fmt.Errorf("encode worker lock: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = lock.Release()
		return nil, false, fmt.Errorf("reset worker lock: %w", err)
	}
	if _, err := file.WriteAt(record, 0); err != nil {
		_ = lock.Release()
		return nil, false, fmt.Errorf("write worker lock: %w", err)
	}
	return lock, true, nil
}

func (l *WorkerLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return l.store.releaseWorker(l.runID, file)
}

func (s *Store) releaseWorker(runID string, owned *os.File) error {
	lock, err := acquireStoreLockWait(filepath.Join(s.dir, ".claim.lock"))
	if err != nil {
		return err
	}
	defer lock.Release()
	if owned == nil {
		var held bool
		owned, held, err = s.tryWorkerLock(runID)
		if err != nil || !held {
			return err
		}
	}
	_ = unlockStoreFile(owned)
	closeErr := owned.Close()
	removeErr := os.Remove(s.workerLockPath(runID))
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if closeErr != nil {
		return fmt.Errorf("close worker lock: %w", closeErr)
	}
	return removeErr
}

func (s *Store) Handoff(runID, fingerprint string) (Registration, bool, error) {
	lock, held, err := s.acquireClaimLock()
	if err != nil {
		return Registration{}, false, err
	}
	if !held {
		return Registration{}, false, nil
	}
	defer lock.Release()
	reg, found, err := s.Get(runID)
	if err != nil || !found || reg.Phase != PhaseWatching {
		return reg, false, err
	}
	if reg.Fingerprint == fingerprint {
		reg.Phase = PhaseAwaitingUser
	} else {
		reg.Phase = PhaseHandoffInProgress
		reg.Fingerprint = fingerprint
	}
	reg.Error = ""
	reg.UpdatedAt = time.Now().UTC().Unix()
	if err := s.write(reg); err != nil {
		return Registration{}, false, err
	}
	return reg, reg.Phase == PhaseHandoffInProgress, nil
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

func (s *Store) createWorkerLock(path string) (bool, error) {
	record, err := json.Marshal(workerLockRecord{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		return false, fmt.Errorf("encode worker lock: %w", err)
	}
	if err := os.WriteFile(path, record, 0o600); err != nil {
		return false, fmt.Errorf("create worker lock: %w", err)
	}
	return true, nil
}

func (s *Store) tryWorkerLock(runID string) (*os.File, bool, error) {
	file, err := os.OpenFile(s.workerLockPath(runID), os.O_RDWR, 0o600)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open worker lock: %w", err)
	}
	if err := tryLockStoreFile(file); err != nil {
		_ = file.Close()
		return nil, false, nil
	}
	return file, true, nil
}

func (s *Store) workerLockExpired(path string) bool {
	data, err := os.ReadFile(path)
	if err == nil {
		var record workerLockRecord
		if json.Unmarshal(data, &record) == nil && !record.StartedAt.IsZero() {
			return time.Since(record.StartedAt) >= workerStartGrace
		}
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) >= workerStartGrace
}
