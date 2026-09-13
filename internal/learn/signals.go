package learn

import (
	"regexp"
	"strings"
)

type SignalType string

const (
	SignalExplicitCorrection SignalType = "explicit_correction"
	SignalUserDiff           SignalType = "user_diff"
	SignalExplicitReject     SignalType = "explicit_reject"
	SignalStatedPreference   SignalType = "stated_preference"
	SignalExplicitPrefer     SignalType = "explicit_prefer"
	SignalExplicitAccept     SignalType = "explicit_accept"
	SignalModelEdit          SignalType = "model_edit"
	SignalImported           SignalType = "imported"
	SignalLLMDistilled       SignalType = "llm_distilled"
)

var Weights = map[SignalType]float64{
	SignalExplicitCorrection: 1.0,
	SignalUserDiff:           0.6,
	SignalExplicitReject:     0.9,
	SignalStatedPreference:   0.8,
	SignalExplicitPrefer:     0.8,
	SignalExplicitAccept:     0.6,
	SignalModelEdit:          0.4,
	SignalImported:           0.4,
}

type Signal struct {
	Type           SignalType `json:"type"`
	Statement      string     `json:"statement"`
	Category       string     `json:"category"`
	File           string     `json:"file,omitempty"`
	Before         string     `json:"before,omitempty"`
	After          string     `json:"after,omitempty"`
	TriggerContext string     `json:"trigger_context,omitempty"`
	// Weight is the per-statement confidence seed used only by
	// SignalLLMDistilled; every other signal type gets its weight from
	// cfg.WeightFor instead.
	Weight float64 `json:"weight,omitempty"`
	// Evidence is the short quote/paraphrase backing a distilled statement.
	// It flows into the events log, never into the stored statement.
	Evidence string `json:"evidence,omitempty"`
}

var statedPrefRe = regexp.MustCompile(`(?i)(?:i prefer|prefer|always use|never use|use \w+ (?:over|instead of)|my style is)[^.!?\n]*`)

var correctionRe = regexp.MustCompile(`(?i)(?:wrong|incorrect|should be|should have|fix|instead|actually|no,)`)

func ExtractStatedPreference(text string) (string, string) {
	m := statedPrefRe.FindString(text)
	if m == "" {
		return "", ""
	}
	m = strings.TrimSpace(m)
	cat := inferCategory(m)
	return m, cat
}

func IsCorrectionShaped(text string) bool {
	return correctionRe.MatchString(text)
}

func inferCategory(s string) string {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "test") || strings.Contains(low, "spec"):
		return "Testing"
	case strings.Contains(low, "import") || strings.Contains(low, "require"):
		return "Imports"
	case strings.Contains(low, "error") || strings.Contains(low, "handle"):
		return "Error Handling"
	case strings.Contains(low, "naming") || strings.Contains(low, "variable") || strings.Contains(low, "function"):
		return "Naming"
	case strings.Contains(low, "type") || strings.Contains(low, "interface") || strings.Contains(low, "generic"):
		return "Types"
	case strings.Contains(low, "async") || strings.Contains(low, "await") || strings.Contains(low, "promise"):
		return "Async"
	default:
		return "General"
	}
}

var categoryKeywords = map[string][]string{
	"Testing":       {"test", "spec", "mock", "assert", "coverage"},
	"Imports":       {"import", "require", "export"},
	"Error Handling": {"error", "throw", "catch", "handle"},
	"Naming":        {"naming", "variable", "function", "camel", "snake"},
	"Types":         {"type", "interface", "generic", "struct"},
	"Async":         {"async", "await", "promise", "callback"},
}

func InferCategoryFromStatement(stmt string) string {
	return inferCategory(stmt)
}

// ExtractCandidates turns a user edit into at most one user_diff signal. Without a
// baseline there is nothing to compare, so nothing is learned; the statement
// carries only the lines the user added, never the file head.
func ExtractCandidates(diffBefore, diffAfter string) []Signal {
	if strings.TrimSpace(diffBefore) == strings.TrimSpace(diffAfter) {
		return nil
	}
	stmt := summarizeDiff(diffBefore, diffAfter)
	if stmt == "" {
		return nil
	}
	return []Signal{{
		Type:      SignalUserDiff,
		Statement: stmt,
		Category:  inferCategory(diffAfter),
		Before:    diffBefore,
		After:     diffAfter,
	}}
}

const maxStatementRunes = 160

// summarizeDiff returns "prefers: <added lines>" built from the lines present in
// after but absent from before, or "" when there is no baseline or nothing was added.
func summarizeDiff(before, after string) string {
	if strings.TrimSpace(before) == "" {
		return ""
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(before, "\n") {
		seen[strings.Join(strings.Fields(l), " ")] = true
	}
	var added []string
	for _, l := range strings.Split(after, "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" || seen[l] {
			continue
		}
		added = append(added, l)
	}
	if len(added) == 0 {
		return ""
	}
	return TruncateRunes("prefers: "+strings.Join(added, " / "), maxStatementRunes)
}

// TruncateRunes cuts on rune boundaries and appends an ellipsis when it cut.
func TruncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
