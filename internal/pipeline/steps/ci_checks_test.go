package steps

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func TestHasPendingChecksIncludesLegacyStatuses(t *testing.T) {
	t.Parallel()
	for _, check := range []scm.Check{
		{Name: "CodeRabbit", Bucket: scm.CheckBucketPending},
		{Name: "unit", Bucket: scm.CheckBucketPending},
	} {
		if !hasPendingChecks([]scm.Check{check}) {
			t.Fatalf("pending check %q must keep CI running", check.Name)
		}
	}
}

func TestQueuedPendingChecksRequiresEveryPendingCheckToBeExplicitlyQueued(t *testing.T) {
	t.Parallel()
	queuedAt := time.Date(2026, time.July, 16, 8, 44, 0, 0, time.UTC)

	checks := []scm.Check{
		{Name: "cursor", Bucket: scm.CheckBucketPending, Progress: scm.CheckProgressQueued, CreatedAt: queuedAt},
		{Name: "claude", Bucket: scm.CheckBucketPending, Progress: scm.CheckProgressQueued, CreatedAt: queuedAt.Add(time.Minute)},
		{Name: "unit", Bucket: scm.CheckBucketPass},
	}
	names, oldest, ok := queuedPendingChecks(checks)
	if !ok {
		t.Fatal("explicitly queued pending checks should be eligible for the attention guard")
	}
	if got, want := names, "claude, cursor"; got != want {
		t.Fatalf("queued names = %q, want %q", got, want)
	}
	if !oldest.Equal(queuedAt) {
		t.Fatalf("oldest queued time = %v, want %v", oldest, queuedAt)
	}

	checks[1].Progress = scm.CheckProgressRunning
	if _, _, ok := queuedPendingChecks(checks); ok {
		t.Fatal("an in-progress check must keep the monitor waiting instead of raising the queued-only guard")
	}
}

func TestCIQueuedChecksOutcomeRequiresAttention(t *testing.T) {
	t.Parallel()

	outcome := ciQueuedChecksOutcome("claude, cursor", 10*time.Minute)
	if !outcome.NeedsApproval {
		t.Fatal("queued checks must park for attention")
	}
	var findings Findings
	if err := json.Unmarshal([]byte(outcome.Findings), &findings); err != nil {
		t.Fatalf("unmarshal findings: %v", err)
	}
	if !strings.Contains(findings.Summary, "queued") || len(findings.Items) != 1 || !strings.Contains(findings.Items[0].Description, "claude, cursor") {
		t.Fatalf("unexpected queued-check finding: %+v", findings)
	}
}

func TestPendingCheckMatchesLastFixed_SpecialCheckNames(t *testing.T) {
	t.Parallel()

	lastFixedChecks := encodeLastFixedChecks([]string{"lint,unit", "deploy+conflict"}, true)
	checks := []scm.Check{
		{Name: "lint,unit", Bucket: "pending"},
	}

	if !pendingCheckMatchesLastFixed(checks, lastFixedChecks) {
		t.Fatalf("expected pending check with special characters to match encoded last fixed checks %q", lastFixedChecks)
	}

	checks = []scm.Check{
		{Name: "lint", Bucket: "pending"},
	}
	if pendingCheckMatchesLastFixed(checks, lastFixedChecks) {
		t.Fatalf("expected unrelated pending check not to match encoded last fixed checks %q", lastFixedChecks)
	}
}
