package learn

import (
	"os"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/safety"
)

// RulebookOverlap is the provenance tag marking a preference row that merely
// restates a rule from the user's curated rulebook (config key `rulebook:`).
// The rulebook is authoritative and always loaded, so the learned copy only
// burns render tokens: render suppresses tagged rows and the consolidator
// retires them. The tag rides the existing provenance JSON array (the same
// mechanism merge uses) so no schema change and no scan churn is needed.
const RulebookOverlap = "rulebook_overlap"

// rulebookMaxBytes caps the rulebook text sent to the gateway: a curated
// rulebook is a few KB of rules, and an unbounded paste would dwarf the
// candidate/evidence sections. The cut lands on a line boundary.
const rulebookMaxBytes = 8 << 10

// LoadRulebook returns the curated rulebook as one rule per line, every line
// run through the secret scanner (redacted like evidence — nothing
// secret-shaped leaves the machine via the rulebook either) and the whole
// capped at rulebookMaxBytes. Fail-open: an unset, unreadable, or empty
// rulebook returns ("", false) and every caller treats the feature as off.
func LoadRulebook(cfg config.Config) (string, bool) {
	if strings.TrimSpace(cfg.Rulebook) == "" {
		return "", false
	}
	b, err := os.ReadFile(cfg.Rulebook)
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue // blank lines carry no rule and only grow the prompt
		}
		if safety.ContainsSecret(line) {
			kept = append(kept, "[redacted secret]")
			continue
		}
		kept = append(kept, line)
	}
	block := strings.Join(kept, "\n")
	if len(block) > rulebookMaxBytes {
		block = block[:rulebookMaxBytes]
		if idx := strings.LastIndex(block, "\n"); idx > 0 {
			block = block[:idx]
		}
		block = strings.TrimSpace(block)
	}
	if block == "" {
		return "", false
	}
	return block, true
}

// HasRulebookOverlap reports whether a preference's provenance carries the
// rulebook_overlap tag. Provenance is a JSON array of strings, so the tag is
// matched as a quoted JSON string — the same shape merge writes via
// json_insert.
func HasRulebookOverlap(provenance string) bool {
	return strings.Contains(provenance, `"`+RulebookOverlap+`"`)
}
