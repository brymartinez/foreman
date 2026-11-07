package agent

import (
	"strings"
	"testing"
)

func TestImplementerVerifiesTheCheckedAcceptanceCriteria(t *testing.T) {
	for _, correction := range []bool{false, true} {
		b := localBrief()
		b.PlanText = "## Requirements\n\nR1. Retry 5xx."
		b.Correction = correction
		p := ImplementationPrompt(b)
		for _, want := range []string{"R1. Retry 5xx.", "Implement every acceptance criterion", "verify each criterion", "Report the validation you ran"} {
			if !strings.Contains(p, want) {
				t.Errorf("correction %t: prompt lacks %q", correction, want)
			}
		}
	}
}
