// Package skills evaluates provider-independent evidence into skill states.
package skills

import (
	"math"

	"github.com/forge/forge/internal/db"
)

const (
	ScopeProject = "project"
	ScopeGlobal  = "global"

	StateUnobserved   = "unobserved"
	StateSuspectedGap = "suspected_gap"
	StateLearning     = "learning"
	StateApplied      = "applied"
	StateReliable     = "reliable"
	StateRegressed    = "regressed"

	minimumActionConfidence   = 0.70
	minimumReliableConfidence = 0.75
	strongNegativeConfidence  = 0.85
	repeatedNegativeThreshold = 2
	repeatedSuccessThreshold  = 2
	counterEvidencePenalty    = 0.15
)

// EvidenceSummary contains normalized counts, never provider payloads.
type EvidenceSummary struct {
	Confidence             float64
	SupportingEvidence     int
	CounterEvidence        int
	SuccessfulApplications int
	FailedApplications     int
	Accepted               bool
	CrossSessionSuccess    bool
	Transferable           bool
	Normalized             bool
	ObservedAt             string
}

// Evaluate derives a next state without performing I/O. Project states accept
// project-local evidence; global states accept only normalized transferable
// successes, preserving the provenance of every source observation locally.
func Evaluate(current db.SkillState, summary EvidenceSummary) (db.SkillState, bool) {
	next := current
	if next.State == "" {
		next.State = StateUnobserved
	}
	if next.ScopeType == ScopeGlobal && (!summary.Transferable || !summary.Normalized || summary.SuccessfulApplications == 0 ||
		summary.CounterEvidence > 0 || summary.FailedApplications > 0) {
		return next, next != current
	}

	previous := next
	newEvidence := summary.SupportingEvidence + summary.CounterEvidence
	next.EvidenceCount += newEvidence
	next.SuccessfulApplications += summary.SuccessfulApplications
	next.FailedApplications += summary.FailedApplications
	if summary.ObservedAt != "" {
		next.LastObservedAt = summary.ObservedAt
	}
	next.Confidence = confidenceAfter(previous, summary, newEvidence)
	next.State = stateAfter(previous, next, summary)
	return next, next != current
}

func confidenceAfter(current db.SkillState, summary EvidenceSummary, newEvidence int) float64 {
	confidence := current.Confidence
	if summary.SupportingEvidence > 0 {
		priorWeight := current.EvidenceCount
		confidence = (confidence*float64(priorWeight) + summary.Confidence*float64(summary.SupportingEvidence)) /
			float64(priorWeight+summary.SupportingEvidence)
	} else if current.EvidenceCount == 0 && summary.CounterEvidence == 0 {
		confidence = summary.Confidence
	}
	confidence -= float64(summary.CounterEvidence) * counterEvidencePenalty
	return math.Max(0, math.Min(1, confidence))
}

func stateAfter(current, next db.SkillState, summary EvidenceSummary) string {
	if summary.FailedApplications > 0 && summary.Confidence >= strongNegativeConfidence &&
		(current.State == StateLearning || current.State == StateApplied || current.State == StateReliable) {
		return StateRegressed
	}
	if summary.Accepted && summary.Confidence >= minimumActionConfidence {
		return StateLearning
	}
	if next.SuccessfulApplications >= repeatedSuccessThreshold && summary.CrossSessionSuccess && summary.Confidence >= minimumReliableConfidence {
		return StateReliable
	}
	if summary.SuccessfulApplications > 0 && summary.Confidence >= minimumActionConfidence {
		return StateApplied
	}
	if next.FailedApplications >= repeatedNegativeThreshold && summary.Confidence >= minimumActionConfidence {
		return StateSuspectedGap
	}
	return current.State
}
