package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Stable reasons emitted when autonomous review must park for a decision.
// They are intentionally strings rather than new run states: the existing
// approval gate remains the durable state machine and the reason is carried in
// its structured finding.
const (
	ReviewProgressDiminishingReason = "diminishing-progress"
	ReviewProgressBudgetReason      = "review-time-budget"
)

// ReviewProgressGuard measures deterministic progress in one review/fix loop.
// A new commit/head or a materially different finding set resets the
// no-progress clock. Log chatter and generated finding IDs do not.
type ReviewProgressGuard struct {
	startedAt         time.Time
	lastProgressAt    time.Time
	lastHead          string
	lastFindingDigest string
	noProgressTimeout time.Duration
	maxDuration       time.Duration
	rounds            int
}

// NewReviewProgressGuard starts a guard at start. Durations are expected to be
// positive and are kept as supplied so tests and callers can choose a bounded
// policy explicitly.
func NewReviewProgressGuard(start time.Time, noProgressTimeout, maxDuration time.Duration) *ReviewProgressGuard {
	return &ReviewProgressGuard{
		startedAt:         start,
		lastProgressAt:    start,
		noProgressTimeout: noProgressTimeout,
		maxDuration:       maxDuration,
	}
}

// Observe records one completed review round. Progress is based on a new
// recorded head or a changed semantic finding digest; presentation changes and
// model-generated IDs are deliberately ignored.
func (g *ReviewProgressGuard) Observe(headSHA, findingsJSON string, now time.Time) {
	if g == nil {
		return
	}
	g.rounds++
	digest := reviewFindingDigest(findingsJSON)
	if g.rounds == 1 || !strings.EqualFold(strings.TrimSpace(headSHA), strings.TrimSpace(g.lastHead)) || digest != g.lastFindingDigest {
		g.lastProgressAt = now
	}
	g.lastHead = strings.TrimSpace(headSHA)
	g.lastFindingDigest = digest
}

// Reset marks an explicit human response as progress. Approval waits are not
// autonomous work and therefore must not consume the no-progress window.
func (g *ReviewProgressGuard) Reset(now time.Time) {
	if g == nil {
		return
	}
	g.lastProgressAt = now
}

// StopReason reports whether the guard has expired at now.
func (g *ReviewProgressGuard) StopReason(now time.Time) (string, bool) {
	if g == nil {
		return "", false
	}
	if g.maxDuration > 0 && now.Sub(g.startedAt) >= g.maxDuration {
		return ReviewProgressBudgetReason, true
	}
	if g.noProgressTimeout > 0 && now.Sub(g.lastProgressAt) >= g.noProgressTimeout {
		return ReviewProgressDiminishingReason, true
	}
	return "", false
}

// Remaining returns the next deadline for the guard. A zero duration means the
// guard is already expired; a negative duration means no configured deadline.
func (g *ReviewProgressGuard) Remaining(now time.Time) time.Duration {
	if g == nil {
		return -1
	}
	var remaining time.Duration
	hasDeadline := false
	if g.maxDuration > 0 {
		remaining = g.maxDuration - now.Sub(g.startedAt)
		hasDeadline = true
	}
	if g.noProgressTimeout > 0 {
		candidate := g.noProgressTimeout - now.Sub(g.lastProgressAt)
		if !hasDeadline || candidate < remaining {
			remaining = candidate
			hasDeadline = true
		}
	}
	if !hasDeadline {
		return -1
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

// AttentionFindings creates a stable, ask-user finding for the existing gate
// path. It is intentionally not a new persisted run status or merge state.
func (g *ReviewProgressGuard) AttentionFindings(now time.Time) string {
	reason, _ := g.StopReason(now)
	if reason == "" {
		reason = ReviewProgressDiminishingReason
	}
	findings := types.Findings{
		Items: []types.Finding{{
			ID:          "review-" + reason,
			Severity:    "warning",
			Description: fmt.Sprintf("Autonomous review stopped: %s after %s without new review evidence.", reason, formatReviewDuration(now.Sub(g.lastProgressAt))),
			Action:      types.ActionAskUser,
			Source:      "pipeline",
		}},
		Summary:       "Review parked for human attention: " + reason,
		RiskLevel:     "medium",
		RiskRationale: "The review loop did not produce enough deterministic progress to continue autonomously.",
	}
	raw, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return `{"findings":[{"id":"review-diminishing-progress","severity":"warning","description":"Autonomous review stopped for human attention.","action":"ask-user","source":"pipeline"}],"summary":"Review parked for human attention","risk_level":"medium","risk_rationale":"The review loop stopped without sufficient progress."}`
	}
	return raw
}

func formatReviewDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// reviewFindingDigest normalizes only semantic fields. IDs, summaries and
// ordering are excluded because agents commonly regenerate them between
// otherwise identical review rounds.
func reviewFindingDigest(raw string) string {
	var parsed types.Findings
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return digestString(strings.TrimSpace(raw))
	}
	type semanticFinding struct {
		Severity string `json:"severity"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Desc     string `json:"description"`
		Action   string `json:"action"`
		Source   string `json:"source"`
		Category string `json:"category"`
	}
	items := make([]semanticFinding, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		items = append(items, semanticFinding{
			Severity: strings.TrimSpace(item.Severity),
			File:     strings.TrimSpace(item.File),
			Line:     item.Line,
			Desc:     strings.Join(strings.Fields(item.Description), " "),
			Action:   strings.TrimSpace(item.Action),
			Source:   strings.TrimSpace(item.Source),
			Category: strings.TrimSpace(item.Category),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := json.Marshal(items[i])
		right, _ := json.Marshal(items[j])
		return string(left) < string(right)
	})
	encoded, err := json.Marshal(items)
	if err != nil {
		return digestString(strings.TrimSpace(raw))
	}
	return digestString(string(encoded))
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
