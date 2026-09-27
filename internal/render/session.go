package render

import (
	"fmt"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
)

// SessionHeader is the SessionStart prewarm block's opening line: the whole
// budgeted profile, never a ranking cut, so its wording never needs an
// "N shown" variant (req 2).
func SessionHeader(cfg config.Config, n int) string {
	return fmt.Sprintf("<palate-prewarm scope=\"session\">\nYour taste profile — %d preferences, the whole profile (also in %s). Apply them by default; the current request wins.\n", n, cfg.TastePath())
}

// SessionTail closes the block SessionHeader opens.
func SessionTail() string { return "</palate-prewarm>" }

// sessionHeaderOverheadN is the worst-case preference count used only to
// size SessionOverhead, never to render it — six digits so a real profile
// (never in the hundreds of thousands) can't make the estimate low (req 1).
const sessionHeaderOverheadN = 999999

// SessionOverhead is the SessionStart block's fixed header+tail cost in
// estimated tokens, at the worst-case width for N, so the profile budget
// never depends on the selection's own output.
func SessionOverhead(cfg config.Config) int {
	return learn.EstimateTokens(SessionHeader(cfg, sessionHeaderOverheadN)) + learn.EstimateTokens(SessionTail())
}
