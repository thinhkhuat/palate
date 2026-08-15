package skills

import (
	"fmt"
	"regexp"
	"strings"
)

var testableRe = regexp.MustCompile(`(?i)(?:should|must|will|when|if).{10,}`)

func ValidateRumination(whyBetter string) error {
	whyBetter = strings.TrimSpace(whyBetter)
	if len(whyBetter) < 40 {
		return fmt.Errorf("why_better too short: %d chars, need >=40", len(whyBetter))
	}
	if !testableRe.MatchString(whyBetter) {
		return fmt.Errorf("why_better must state a testable prediction (include should/must/will/when/if + >=10 chars)")
	}
	return nil
}

func CheckRevision(oldContent, newContent, whyBetter string) error {
	if strings.TrimSpace(oldContent) == strings.TrimSpace(newContent) {
		return fmt.Errorf("revision is cosmetic: content unchanged")
	}
	return ValidateRumination(whyBetter)
}
