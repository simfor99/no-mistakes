package supervision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreArmThenClaimStopHook(t *testing.T) {
	store := NewStore(t.TempDir())
	armed, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"})
	if err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	if armed.Phase != PhaseArmed {
		t.Fatalf("Arm() phase = %q, want %q", armed.Phase, PhaseArmed)
	}

	claimed, ok, err := store.Claim("/work", "session-1")
	if err != nil || !ok {
		t.Fatalf("Claim() = (%+v, %v, %v), want claimed registration", claimed, ok, err)
	}
	if claimed.SessionID != "session-1" || claimed.Phase != PhaseWatching {
		t.Fatalf("Claim() = %+v, want session and watching phase", claimed)
	}

	_, ok, err = store.Claim("/work", "session-2")
	if err != nil || ok {
		t.Fatalf("second Claim() = (_, %v, %v), want not claimed", ok, err)
	}
}

func TestStoreClaimRejectsDifferentWorkingDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"}); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	if _, ok, err := store.Claim("/other", "session-1"); err != nil || ok {
		t.Fatalf("Claim(other cwd) = (_, %v, %v), want no claim", ok, err)
	}
}

func TestStoreArmRejectsAnotherActiveRegistrationInSameWorkingDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"}); err != nil {
		t.Fatalf("first Arm() error = %v", err)
	}
	if _, err := store.Arm(Registration{RunID: "run-2", RepoID: "repo-1", CWD: "/work"}); err == nil {
		t.Fatal("second Arm() error = nil, want active-registration rejection")
	}
}

func TestStoreFindByCWDKeepsAwaitingUserRegistration(t *testing.T) {
	store := NewStore(t.TempDir())
	reg, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"})
	if err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	reg.Phase = PhaseAwaitingUser
	if err := store.Save(reg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, found, err := store.FindByCWD("/work")
	if err != nil || !found || got.RunID != "run-1" {
		t.Fatalf("FindByCWD() = (%+v, %v, %v), want awaiting registration", got, found, err)
	}
}

func TestStoreUsesRunScopedFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"}); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	if got, want := store.Path("run-1"), filepath.Join(dir, "run-1.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestStoreWorkerLockIsExclusive(t *testing.T) {
	store := NewStore(t.TempDir())
	if got, err := store.AcquireWorker("run-1"); err != nil || !got {
		t.Fatalf("first AcquireWorker() = (%v, %v), want (true, nil)", got, err)
	}
	if got, err := store.AcquireWorker("run-1"); err != nil || got {
		t.Fatalf("second AcquireWorker() = (%v, %v), want (false, nil)", got, err)
	}
	if err := store.ReleaseWorker("run-1"); err != nil {
		t.Fatalf("ReleaseWorker() error = %v", err)
	}
	if got, err := store.AcquireWorker("run-1"); err != nil || !got {
		t.Fatalf("AcquireWorker() after release = (%v, %v), want (true, nil)", got, err)
	}
}

func TestStoreWorkerLockRecoversExpiredStarter(t *testing.T) {
	store := NewStore(t.TempDir())
	path := store.workerLockPath("run-1")
	record, err := json.Marshal(workerLockRecord{PID: 1234, StartedAt: time.Now().Add(-workerStartGrace - time.Second)})
	if err != nil {
		t.Fatalf("marshal worker lock record: %v", err)
	}
	if err := os.WriteFile(path, record, 0o600); err != nil {
		t.Fatalf("write stale worker lock: %v", err)
	}

	got, err := store.AcquireWorker("run-1")
	if err != nil || !got {
		t.Fatalf("AcquireWorker() after stale starter = (%v, %v), want (true, nil)", got, err)
	}
}

func TestStoreHoldWorkerReleasesMarker(t *testing.T) {
	store := NewStore(t.TempDir())
	if got, err := store.AcquireWorker("run-1"); err != nil || !got {
		t.Fatalf("AcquireWorker() = (%v, %v), want (true, nil)", got, err)
	}
	lock, held, err := store.HoldWorker("run-1")
	if err != nil || !held {
		t.Fatalf("HoldWorker() = (%v, %v, %v), want held lock", lock, held, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if got, err := store.AcquireWorker("run-1"); err != nil || !got {
		t.Fatalf("AcquireWorker() after worker release = (%v, %v), want (true, nil)", got, err)
	}
}

func TestStoreHandoffRejectsRepeatedFingerprint(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Arm(Registration{RunID: "run-1", RepoID: "repo-1", CWD: "/work"}); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}
	if _, ok, err := store.Claim("/work", "session-1"); err != nil || !ok {
		t.Fatalf("Claim() = (_, %v, %v), want claimed registration", ok, err)
	}
	first, resume, err := store.Handoff("run-1", "same-event")
	if err != nil || !resume || first.Phase != PhaseHandoffInProgress {
		t.Fatalf("first Handoff() = (%+v, %v, %v), want resumable handoff", first, resume, err)
	}
	first.Phase = PhaseWatching
	if err := store.Save(first); err != nil {
		t.Fatalf("restore watching phase: %v", err)
	}
	second, resume, err := store.Handoff("run-1", "same-event")
	if err != nil || resume || second.Phase != PhaseAwaitingUser {
		t.Fatalf("repeated Handoff() = (%+v, %v, %v), want non-resuming awaiting user", second, resume, err)
	}
}
