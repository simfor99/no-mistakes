package pipeline

import (
	"strings"
	"testing"
	"time"
)

func TestReviewProgressGuardStopsAfterRepeatedFindingDigest(t *testing.T) {
	start := time.Unix(100, 0)
	guard := NewReviewProgressGuard(start, 10*time.Minute, 45*time.Minute)

	guard.Observe("head-a", `{"findings":[{"severity":"warning","file":"a.go","line":4,"description":"same","action":"auto-fix"}]}`, start)
	if reason, stopped := guard.StopReason(start.Add(9 * time.Minute)); stopped || reason != "" {
		t.Fatalf("guard stopped too early: reason=%q stopped=%v", reason, stopped)
	}

	guard.Observe("head-a", `{"findings":[{"severity":"warning","file":"a.go","line":4,"description":"same","action":"auto-fix"}]}`, start.Add(10*time.Minute))
	reason, stopped := guard.StopReason(start.Add(20 * time.Minute))
	if !stopped || reason != ReviewProgressDiminishingReason {
		t.Fatalf("StopReason = %q/%v, want %q/true", reason, stopped, ReviewProgressDiminishingReason)
	}
}

func TestReviewProgressGuardResetsOnHeadOrFindingProgress(t *testing.T) {
	start := time.Unix(200, 0)
	guard := NewReviewProgressGuard(start, 10*time.Minute, 45*time.Minute)
	first := `{"findings":[{"severity":"warning","file":"a.go","line":4,"description":"same","action":"auto-fix"}]}`
	second := `{"findings":[{"severity":"warning","file":"b.go","line":8,"description":"new","action":"auto-fix"}]}`

	guard.Observe("head-a", first, start)
	guard.Observe("head-b", first, start.Add(9*time.Minute))
	if reason, stopped := guard.StopReason(start.Add(18 * time.Minute)); stopped || reason != "" {
		t.Fatalf("head progress did not reset guard: reason=%q stopped=%v", reason, stopped)
	}

	guard.Observe("head-b", second, start.Add(19*time.Minute))
	if reason, stopped := guard.StopReason(start.Add(28 * time.Minute)); stopped || reason != "" {
		t.Fatalf("finding progress did not reset guard: reason=%q stopped=%v", reason, stopped)
	}
}

func TestReviewProgressGuardStopsAtMaximumDuration(t *testing.T) {
	start := time.Unix(300, 0)
	guard := NewReviewProgressGuard(start, 30*time.Minute, 20*time.Minute)
	guard.Observe("head-a", `{"findings":[]}`, start)

	reason, stopped := guard.StopReason(start.Add(20 * time.Minute))
	if !stopped || reason != ReviewProgressBudgetReason {
		t.Fatalf("StopReason = %q/%v, want %q/true", reason, stopped, ReviewProgressBudgetReason)
	}
}

func TestReviewProgressGuardFindingDigestIgnoresGeneratedIDsAndPresentation(t *testing.T) {
	start := time.Unix(400, 0)
	guard := NewReviewProgressGuard(start, time.Minute, 10*time.Minute)
	first := `{"summary":"one","findings":[{"id":"review-1","severity":"warning","file":"a.go","line":4,"description":"same","action":"auto-fix"}]}`
	second := `{"summary":"still one","findings":[{"id":"other-id","severity":"warning","file":"a.go","line":4,"description":"same","action":"auto-fix"}]}`

	guard.Observe("head-a", first, start)
	guard.Observe("head-a", second, start.Add(30*time.Second))
	reason, stopped := guard.StopReason(start.Add(90 * time.Second))
	if !stopped || reason != ReviewProgressDiminishingReason {
		t.Fatalf("presentation-only change reset guard: reason=%q stopped=%v", reason, stopped)
	}
}

func TestReviewProgressAttentionFindingIsMachineReadable(t *testing.T) {
	start := time.Unix(500, 0)
	guard := NewReviewProgressGuard(start, time.Minute, 10*time.Minute)
	guard.Observe("head-a", `{"findings":[{"severity":"warning","description":"same","action":"auto-fix"}]}`, start)

	findingJSON := guard.AttentionFindings(start.Add(2 * time.Minute))
	if !strings.Contains(findingJSON, `"id":"review-diminishing-progress"`) {
		t.Fatalf("attention finding missing stable id: %s", findingJSON)
	}
	if !strings.Contains(findingJSON, `"action":"ask-user"`) {
		t.Fatalf("attention finding must require a decision: %s", findingJSON)
	}
}
