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
	// Session identifies the session that produced this signal (the harness
	// plus a short hash of the transcript path, Phase 3). It rides the
	// create/reinforce event payload as "session" so learn.SessionCounts can
	// tell "seen again" from "seen twice in the same conversation". Empty
	// when the caller does not know it, in which case no field is written and
	// SessionCounts falls back to the event's UTC calendar day.
	Session string `json:"session,omitempty"`
	// ProjectSpecific marks the statement as true only for the project it was
	// learned in (a path, a repo-specific tool, a project's own naming
	// convention) rather than a portable taste. It writes TagProjectSpecific
	// into provenance on insert, the same mechanism RulebookOverlap uses.
	ProjectSpecific bool `json:"project_specific,omitempty"`
}

// statedPrefRe matches an explicit, first-person statement of preference and
// captures the rest of the sentence. Every trigger is word-bounded and needs
// an object after it: a bare "prefer" used to match inside "preference" /
// "no preference on this" and store the word as a preference (observed
// 2026-09-13 on ordinary chat text).
var statedPrefRe = regexp.MustCompile(`(?i)\b(?:i(?:'d| would| really| strongly)? prefer|we prefer|always use|never use|use \w+ (?:over|instead of)|my style is)\b\s+[^.!?\n]+`)

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

// ExtractCandidates is retired: file-before/after diffs are work product, not
// preferences. Storing the added lines verbatim (the old "prefers: <code>"
// behaviour) poisoned TASTE.md with 53 garbage rows (2026-09-15 audit) that
// read as code dumps, news paragraphs and JSON blobs at identical c=0.30.
// The detached LLM distiller already sees the same edit through the session
// transcript and writes real prose, so the verbatim path only double-learns.
// Kept as a nil-returning stub so callers and tests name the decision point;
// TruncateRunes stays exported for the git-signal path.
func ExtractCandidates(diffBefore, diffAfter string) []Signal {
	return nil
}

const maxStatementRunes = 160

// summarizeDiff is retired alongside ExtractCandidates (see above): kept only
// because the name is referenced in history and reviews. It returns "" always.
func summarizeDiff(before, after string) string {
	return ""
}

// TruncateRunes cuts on rune boundaries and appends an ellipsis when it cut.
func TruncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
