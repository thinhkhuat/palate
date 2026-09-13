package distill

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DistilledStatement is one preference the model extracted from evidence.
type DistilledStatement struct {
	Statement  string  `json:"statement"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"`
}

// ParseResponse tries, in order: (1) unmarshal into the requested
// {"preferences":[...]} envelope; (2) on failure, unmarshal directly into
// a bare array (tolerates a model replying with the previous, unwrapped
// shape); (3) on both failures, return the error. There is no looser,
// per-element regex/bracket-balance fallback — a lenient fallback would
// silently trust truncated output; the correct response to unparseable
// output is Gateway.Complete's retry policy, not a more forgiving parser.
//
// {"preferences": []}, [], and "no changes" (with or without a wrapping
// markdown code fence) all yield (nil, nil). Confidence clamps to
// [0.0, 1.0]; a statement that is empty after strings.TrimSpace is
// dropped. ParseResponse never panics on malformed input.
func ParseResponse(raw string) ([]DistilledStatement, error) {
	trimmed := stripCodeFence(strings.TrimSpace(raw))
	if trimmed == "" {
		return nil, nil
	}
	if strings.EqualFold(strings.Trim(trimmed, `"`), "no changes") {
		return nil, nil
	}

	var env struct {
		Preferences []DistilledStatement `json:"preferences"`
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err == nil {
		return cleanStatements(env.Preferences), nil
	}

	var arr []DistilledStatement
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		return cleanStatements(arr), nil
	}

	return nil, fmt.Errorf("distill: response is neither a {\"preferences\":[...]} envelope nor a bare array: %s", truncateForError(trimmed, 200))
}

// stripCodeFence removes a leading/trailing ``` or ```json markdown fence
// when present. It is formatting cleanup ahead of the same two-tier parse
// above, not a third, looser parsing strategy.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		lang := strings.TrimSpace(s[:idx])
		if lang == "" || strings.EqualFold(lang, "json") {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func cleanStatements(in []DistilledStatement) []DistilledStatement {
	var out []DistilledStatement
	for _, s := range in {
		s.Statement = strings.TrimSpace(s.Statement)
		if s.Statement == "" {
			continue
		}
		if s.Confidence < 0 {
			s.Confidence = 0
		} else if s.Confidence > 1 {
			s.Confidence = 1
		}
		out = append(out, s)
	}
	return out
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
