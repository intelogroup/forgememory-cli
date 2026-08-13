package skills

import (
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestEvaluateVerificationStateTransitions(t *testing.T) {
	current := db.SkillState{SkillKey: "verification.pre_ship", ScopeType: "project", ScopeID: "project-a", State: StateUnobserved}

	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.40, SupportingEvidence: 1})
	if current.State != StateUnobserved {
		t.Fatalf("weak evidence state = %q, want %q", current.State, StateUnobserved)
	}

	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.80, FailedApplications: 2, CounterEvidence: 2})
	if current.State != StateSuspectedGap {
		t.Fatalf("repeated negatives state = %q, want %q", current.State, StateSuspectedGap)
	}

	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.80, Accepted: true, SupportingEvidence: 1})
	if current.State != StateLearning {
		t.Fatalf("accepted coaching state = %q, want %q", current.State, StateLearning)
	}

	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.80, SuccessfulApplications: 1, SupportingEvidence: 1})
	if current.State != StateApplied {
		t.Fatalf("first success state = %q, want %q", current.State, StateApplied)
	}

	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.85, SuccessfulApplications: 1, SupportingEvidence: 1, CrossSessionSuccess: true})
	if current.State != StateReliable {
		t.Fatalf("cross-session success state = %q, want %q", current.State, StateReliable)
	}

	beforeCounter := current.Confidence
	current = evaluateState(t, current, EvidenceSummary{Confidence: 0.90, FailedApplications: 1, CounterEvidence: 1})
	if current.State != StateRegressed {
		t.Fatalf("strong negative state = %q, want %q", current.State, StateRegressed)
	}
	if current.Confidence >= beforeCounter {
		t.Fatalf("counter-evidence confidence = %v, want less than %v", current.Confidence, beforeCounter)
	}
}

func TestEvaluatePreservesHistoryAndIgnoresWeakSingleNegative(t *testing.T) {
	current := db.SkillState{SkillKey: "verification.pre_ship", ScopeType: "project", ScopeID: "project-a", State: StateUnobserved, Confidence: 0.60, EvidenceCount: 3, SuccessfulApplications: 1, FailedApplications: 0}
	next := evaluateState(t, current, EvidenceSummary{Confidence: 0.40, FailedApplications: 1, CounterEvidence: 1})
	if next.State != StateUnobserved {
		t.Fatalf("weak single negative state = %q, want %q", next.State, StateUnobserved)
	}
	if next.EvidenceCount != 4 || next.SuccessfulApplications != 1 || next.FailedApplications != 1 {
		t.Fatalf("historical counts = %#v", next)
	}
	if next.Confidence >= current.Confidence {
		t.Fatalf("counter-evidence confidence = %v, want less than %v", next.Confidence, current.Confidence)
	}
}

func TestEvaluateOnlyUpdatesGlobalForNormalizedTransferableSuccess(t *testing.T) {
	global := db.SkillState{SkillKey: "verification.pre_ship", ScopeType: ScopeGlobal, ScopeID: "global", State: StateUnobserved}
	next, changed := Evaluate(global, EvidenceSummary{Confidence: 0.90, SuccessfulApplications: 1, SupportingEvidence: 1, Transferable: true})
	if changed || next != global {
		t.Fatalf("unnormalized global result = %#v, changed=%v", next, changed)
	}

	next, changed = Evaluate(global, EvidenceSummary{Confidence: 0.90, SuccessfulApplications: 1, SupportingEvidence: 1, Transferable: true, Normalized: true})
	if !changed || next.ScopeType != ScopeGlobal || next.ScopeID != "global" || next.State != StateApplied {
		t.Fatalf("transferable global result = %#v, changed=%v", next, changed)
	}
}

func TestEvaluateRejectsMixedEvidenceFromGlobalTransfer(t *testing.T) {
	global := db.SkillState{SkillKey: "verification.pre_ship", ScopeType: ScopeGlobal, ScopeID: "global", State: StateUnobserved}
	mixed := EvidenceSummary{
		Confidence:             0.90,
		SupportingEvidence:     1,
		CounterEvidence:        1,
		SuccessfulApplications: 1,
		FailedApplications:     1,
		Transferable:           true,
		Normalized:             true,
	}

	next, changed := Evaluate(global, mixed)
	if changed || next != global {
		t.Fatalf("mixed global result = %#v, changed=%v; want no global transfer", next, changed)
	}
}

func TestEvaluateNormalizesZeroValueStateToUnobserved(t *testing.T) {
	next, changed := Evaluate(db.SkillState{}, EvidenceSummary{})
	if !changed || next.State != StateUnobserved {
		t.Fatalf("zero-value state result = %#v, changed=%v; want state %q", next, changed, StateUnobserved)
	}
}

func evaluateState(t *testing.T, current db.SkillState, summary EvidenceSummary) db.SkillState {
	t.Helper()
	next, changed := Evaluate(current, summary)
	if !changed {
		t.Fatalf("Evaluate(%#v, %#v) did not report a state update", current, summary)
	}
	return next
}
