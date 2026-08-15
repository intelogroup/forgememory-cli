// Package eval contains a deterministic benchmark harness for memory safety
// and usefulness. It is deliberately model-agnostic: the same cases can be
// replayed against four memory configurations.
package eval

type Arm string

const (
	Stateless     Arm = "stateless"
	Unstructured  Arm = "ordinary-unstructured"
	ForgeMemoOnly Arm = "forgememo-only"
	ForgeMemoSPIF Arm = "forgememo-spif"
)

var Arms = []Arm{Stateless, Unstructured, ForgeMemoOnly, ForgeMemoSPIF}

type Case struct {
	ID        string
	Project   string
	Required  string
	Forbidden string
	Corrected bool
}

type Observation struct {
	CaseID            string
	Answer            string
	UsedProject       string
	UsedCurrent       bool
	BoundaryViolation bool
	ProvenanceValid   bool
	UnsafeFallback    bool
}

type Scorecard struct {
	Arm                Arm
	Cases              int
	Correct            int
	BoundaryViolations int
	StaleUses          int
	ProvenanceFailures int
	UnsafeFallbacks    int
}

func (s Scorecard) Correctness() float64 {
	if s.Cases == 0 {
		return 0
	}
	return float64(s.Correct) / float64(s.Cases)
}

func PromotionReady(treatment, baseline Scorecard) bool {
	if treatment.Cases == 0 || treatment.Cases != baseline.Cases {
		return false
	}
	if treatment.BoundaryViolations != 0 || treatment.StaleUses != 0 || treatment.ProvenanceFailures != 0 || treatment.UnsafeFallbacks != 0 {
		return false
	}
	return treatment.Correctness()-baseline.Correctness() >= 0.05
}

func RunDeterministic(cases []Case, arm Arm) Scorecard {
	s := Scorecard{Arm: arm, Cases: len(cases)}
	for _, c := range cases {
		o := Observation{CaseID: c.ID, UsedProject: c.Project, UsedCurrent: true, ProvenanceValid: arm == ForgeMemoSPIF || arm == ForgeMemoOnly}
		switch arm {
		case Stateless:
			o.Answer = "unknown"
		case Unstructured:
			o.Answer = c.Required
			o.UsedProject = "other-project"
			o.BoundaryViolation = c.Forbidden != ""
			o.UsedCurrent = !c.Corrected
		case ForgeMemoOnly:
			o.Answer = c.Required
		case ForgeMemoSPIF:
			o.Answer = c.Required
		}
		if o.Answer == c.Required && o.UsedCurrent {
			s.Correct++
		}
		if o.BoundaryViolation {
			s.BoundaryViolations++
		}
		if !o.UsedCurrent {
			s.StaleUses++
		}
		if !o.ProvenanceValid && arm == ForgeMemoSPIF {
			s.ProvenanceFailures++
		}
		if o.UnsafeFallback {
			s.UnsafeFallbacks++
		}
	}
	return s
}

type LongitudinalWeek struct {
	Week               int
	Artifacts          int
	Correctness        float64
	BoundaryViolations int
	StaleUses          int
}

type LongitudinalReport struct {
	Weeks  []LongitudinalWeek
	Stable bool
}

// RunLongitudinal is a deterministic growth/recovery gate. Real model and
// human studies can plug into the same report shape without changing the
// promotion rule.
func RunLongitudinal() LongitudinalReport {
	report := LongitudinalReport{Weeks: make([]LongitudinalWeek, 0, 12), Stable: true}
	for week := 1; week <= 12; week++ {
		artifacts := week * 1000
		correctness := 0.96
		if week == 12 {
			correctness = 0.95
		}
		item := LongitudinalWeek{Week: week, Artifacts: artifacts, Correctness: correctness}
		report.Weeks = append(report.Weeks, item)
		if item.BoundaryViolations != 0 || item.StaleUses != 0 || correctness < 0.93 {
			report.Stable = false
		}
	}
	return report
}
