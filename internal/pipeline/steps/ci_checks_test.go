package steps

import (
	"testing"

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
