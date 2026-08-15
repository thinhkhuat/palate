package skills

import (
	"testing"
)

func TestRuminationGate(t *testing.T) {
	if err := ValidateRumination("short"); err == nil {
		t.Fatalf("should reject short")
	}
	if err := ValidateRumination("This revision should improve table-driven test generation when the user writes Go tests, because it adds a template."); err != nil {
		t.Fatalf("should accept valid: %v", err)
	}
	if err := CheckRevision("same", "same", "This revision should improve X when Y because Z and it is long enough to pass the length gate for the test."); err == nil {
		t.Fatalf("should reject cosmetic revision")
	}
}

func TestRenderDeterministic(t *testing.T) {
	a := renderSkill("Testing", []string{"use table-driven tests", "prefer testify"})
	b := renderSkill("Testing", []string{"use table-driven tests", "prefer testify"})
	if a != b {
		t.Fatalf("not deterministic")
	}
}
