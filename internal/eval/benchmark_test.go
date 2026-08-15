package eval

import "testing"

func TestFourArmsAreExplicit(t *testing.T) {
	if len(Arms) != 4 || Arms[0] != Stateless || Arms[3] != ForgeMemoSPIF {
		t.Fatalf("arms = %v", Arms)
	}
}

func TestForgeMemoSPIFPassesHardPromotionGates(t *testing.T) {
	cases := []Case{
		{ID: "scope", Project: "alpha", Required: "bounded retries", Forbidden: "beta-secret"},
		{ID: "correction", Project: "alpha", Required: "idempotent writes", Corrected: true},
		{ID: "uncertainty", Project: "alpha", Required: "ask for clarification"},
		{ID: "migration", Project: "alpha", Required: "portable evidence"},
	}
	baseline := RunDeterministic(cases, Unstructured)
	treatment := RunDeterministic(cases, ForgeMemoSPIF)
	if treatment.BoundaryViolations != 0 || treatment.StaleUses != 0 || treatment.ProvenanceFailures != 0 {
		t.Fatalf("treatment safety = %+v", treatment)
	}
	if !PromotionReady(treatment, baseline) {
		t.Fatalf("treatment did not pass promotion gate: baseline=%+v treatment=%+v", baseline, treatment)
	}
}

func TestPromotionBlocksBoundaryOrProvenanceFailure(t *testing.T) {
	good := Scorecard{Arm: ForgeMemoSPIF, Cases: 10, Correct: 10}
	bad := good
	bad.BoundaryViolations = 1
	if PromotionReady(bad, Scorecard{Arm: Unstructured, Cases: 10, Correct: 9}) {
		t.Fatal("boundary failure was averaged away")
	}
	bad = good
	bad.ProvenanceFailures = 1
	if PromotionReady(bad, Scorecard{Arm: Unstructured, Cases: 10, Correct: 9}) {
		t.Fatal("provenance failure was averaged away")
	}
}

func TestTwelveWeekLongitudinalGatePasses(t *testing.T) {
	report := RunLongitudinal()
	if len(report.Weeks) != 12 || !report.Stable {
		t.Fatalf("longitudinal report = %+v", report)
	}
	if report.Weeks[0].Artifacts >= report.Weeks[11].Artifacts {
		t.Fatal("artifact volume did not grow")
	}
	if report.Weeks[11].Correctness < report.Weeks[0].Correctness-0.03 {
		t.Fatal("correctness degraded beyond promotion threshold")
	}
}
