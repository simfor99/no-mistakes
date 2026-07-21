package steps

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func TestHasPendingChecksIgnoresOnlyExplicitAdvisoryLegacyStatus(t *testing.T) {
	t.Parallel()
	if hasPendingChecks([]scm.Check{{Name: "CodeRabbit", Bucket: scm.CheckBucketPending, Source: scm.CheckSourceLegacy, BlocksPending: false}}) {
		t.Fatal("advisory legacy status must not keep CI running")
	}
	if !hasPendingChecks([]scm.Check{{Name: "CodeRabbit", Bucket: scm.CheckBucketPending, Source: scm.CheckSourceLegacy, BlocksPending: true}}) {
		t.Fatal("protected legacy status must keep CI running")
	}
	if !hasPendingChecks([]scm.Check{{Name: "unit", Bucket: scm.CheckBucketPending, Source: scm.CheckSourceNative, BlocksPending: true}}) {
		t.Fatal("native pending check must keep CI running")
	}
}

func TestGitHubPolicyUnavailableRequiresOnlyTheSyntheticBlockingCheck(t *testing.T) {
	t.Parallel()

	policyCheck := scm.Check{
		Name:          "GitHub required-check policy unresolved",
		Bucket:        scm.CheckBucketPending,
		Source:        scm.CheckSourceUnknown,
		BlocksPending: true,
	}
	if !githubPolicyUnavailable(scm.ProviderGitHub, []scm.Check{policyCheck}) {
		t.Fatal("expected GitHub synthetic policy check to require an explicit external-CI decision")
	}
	if githubPolicyUnavailable(scm.ProviderGitLab, []scm.Check{policyCheck}) {
		t.Fatal("non-GitHub providers must not enter the GitHub external-CI gate")
	}
	if githubPolicyUnavailable(scm.ProviderGitHub, []scm.Check{policyCheck, {Name: "build", Bucket: scm.CheckBucketPending, Source: scm.CheckSourceNative, BlocksPending: true}}) {
		t.Fatal("a real pending check must keep normal CI monitoring active")
	}
	if githubPolicyUnavailable(scm.ProviderGitHub, []scm.Check{{Name: policyCheck.Name, Bucket: scm.CheckBucketPending, Source: scm.CheckSourceUnknown, BlocksPending: false}}) {
		t.Fatal("non-blocking synthetic status must not enter the external-CI gate")
	}
}

func TestCIExternalUnavailableOutcomeRequiresExplicitApproval(t *testing.T) {
	t.Parallel()

	outcome := ciExternalUnavailableOutcome()
	if !outcome.NeedsApproval {
		t.Fatal("external CI unavailability must never silently pass")
	}
	if !strings.Contains(outcome.Findings, `"action":"ask-user"`) {
		t.Fatalf("external CI unavailability must be recorded as an ask-user finding: %s", outcome.Findings)
	}
	if !strings.Contains(outcome.Findings, "external_ci_not_run_budget_exhausted") {
		t.Fatalf("external CI receipt marker missing: %s", outcome.Findings)
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
