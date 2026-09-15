package prewarm

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/render"
)

type Block struct {
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
	Scope   string `json:"scope"`
}

func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

func truncateToTokens(s string, budget int) string {
	if estimateTokens(s) <= budget {
		return s
	}
	maxChars := budget * 4
	if len(s) > maxChars {
		return s[:maxChars]
	}
	return s
}

// SessionPrewarm injects the whole active profile — the same bytes as
// TASTE.md's body — so the agent sees every preference the user can see
// (Command Code parity: its <taste> section is the complete file, unranked).
// The budget only ever drops whole lines, and when it does the block says how
// many were left out and where the full file lives.
func SessionPrewarm(conn *sql.DB, cfg config.Config) (Block, error) {
	body, n, err := render.Body(conn, render.RenderOptions{})
	if err != nil {
		return Block{Scope: "session"}, err
	}
	if n == 0 {
		// Command Code tells the agent the same thing when its taste file is
		// empty: the feature exists and fills itself in.
		content := fmt.Sprintf("<palate-prewarm scope=\"session\">\nNo preferences learned yet. Palate learns them automatically from your sessions (the Stop hook distills each turn); to record one now, call palate_prefer / palate_remember or run `palate prefer \"…\"`. Full profile, once there is one: %s\n</palate-prewarm>", cfg.TastePath())
		return Block{Content: content, Tokens: estimateTokens(content), Scope: "session"}, nil
	}
	tail := "</palate-prewarm>"
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	budget := cfg.TokenBudgets.PrewarmSession
	// The header is sized before fitting so the budget accounts for it; the
	// wording differs once lines are dropped so the block never claims "all".
	headFor := func(shown int) string {
		if shown < n {
			return fmt.Sprintf("<palate-prewarm scope=\"session\">\nYour learned taste profile — %d preferences, %d shown here (token budget %d), all of them in %s. Apply them by default; the current request wins.\n", n, shown, budget, cfg.TastePath())
		}
		return fmt.Sprintf("<palate-prewarm scope=\"session\">\nYour learned taste profile — all %d preferences, also in %s. Apply them by default; the current request wins.\n", n, cfg.TastePath())
	}
	// Reserve the header (its truncated form is the longer one), the closing
	// tag, the two joining newlines and one "… N more" line.
	overhead := estimateTokens(headFor(0)) + estimateTokens(tail) + 2 + estimateTokens(fmt.Sprintf("… %d more preferences not shown; read the full profile in %s", n, cfg.TastePath()))
	kept, dropped := fitLines(lines, budget-overhead)
	if dropped > 0 {
		kept = append(kept, fmt.Sprintf("… %d more preferences not shown; read the full profile in %s", dropped, cfg.TastePath()))
	}
	content := headFor(n-dropped) + strings.Join(kept, "\n") + "\n" + tail
	return Block{Content: content, Tokens: estimateTokens(content), Scope: "session"}, nil
}

// fitLines keeps the highest-confidence bullets while they fit the budget.
// render.Body already sorts confidence-desc within each category; here each
// bullet's confidence is parsed from its own "Confidence: 0.xx" tail and the
// candidate pool is ranked globally, so over budget the weakest rows drop
// first no matter which category they sit in. Category headings travel with
// their first kept bullet; a heading with no bullets left under it is dropped.
// dropped counts only preference bullets, since that is the number the reader
// cares about.
func fitLines(lines []string, budget int) (kept []string, dropped int) {
	type bullet struct {
		idx  int
		conf float64
		cost int
		head int
	}
	var bullets []bullet
	for i, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			continue
		}
		bullets = append(bullets, bullet{idx: i, conf: bulletConfidence(l), cost: estimateTokens(l) + 1, head: headingFor(lines, i)})
	}
	sort.SliceStable(bullets, func(a, b int) bool { return bullets[a].conf > bullets[b].conf })
	headCost := map[int]int{}
	for _, bl := range bullets {
		if bl.head >= 0 {
			headCost[bl.head] = estimateTokens(lines[bl.head]) + 1
		}
	}
	keepIdx := map[int]bool{}
	headKept := map[int]bool{}
	used := 0
	for _, bl := range bullets {
		cost := bl.cost
		if bl.head >= 0 && !headKept[bl.head] {
			cost += headCost[bl.head]
		}
		if used+cost > budget {
			continue
		}
		used += cost
		keepIdx[bl.idx] = true
		if bl.head >= 0 {
			keepIdx[bl.head] = true
			headKept[bl.head] = true
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "- ") {
			if keepIdx[i] {
				kept = append(kept, l)
			} else {
				dropped++
			}
			continue
		}
		if strings.HasPrefix(l, "## ") {
			if keepIdx[i] {
				kept = append(kept, l)
			}
			continue
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		kept = append(kept, l)
	}
	for len(kept) > 0 && (strings.HasPrefix(kept[len(kept)-1], "## ") || strings.TrimSpace(kept[len(kept)-1]) == "") {
		kept = kept[:len(kept)-1]
	}
	return kept, dropped
}

// bulletConfidence parses the "Confidence: 0.xx" tail a bullet carries.
// Unparseable lines sort as the weakest so they drop first, never last.
func bulletConfidence(line string) float64 {
	idx := strings.LastIndex(line, "Confidence:")
	if idx < 0 {
		return -1
	}
	var c float64
	if _, err := fmt.Sscanf(line[idx:], "Confidence: %f", &c); err != nil {
		return -1
	}
	return c
}

// headingFor returns the index of the "## Category" heading owning the bullet
// at idx (the nearest heading above it), or -1 when there is none.
func headingFor(lines []string, idx int) int {
	for i := idx - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "## ") {
			return i
		}
	}
	return -1
}

var lastGoalHash string

// Deduper carries goal-dedup state for one caller. Hook handlers are
// one-shot processes (fresh state per invocation); long-lived callers
// (MCP server, daemon) keep one Deduper so repeats within a session
// suppress but separate sessions stay independent.
type Deduper struct {
	lastGoalHash string
}

func (d *Deduper) GoalPrewarm(conn *sql.DB, cfg config.Config, userPrompt string) (Block, bool, error) {
	hash := goalHash(userPrompt)
	if hash == d.lastGoalHash {
		return Block{Scope: "goal"}, false, nil
	}
	d.lastGoalHash = hash
	return goalPrewarmUncached(conn, cfg, userPrompt)
}

func GoalPrewarm(conn *sql.DB, cfg config.Config, userPrompt string) (Block, bool, error) {
	hash := goalHash(userPrompt)
	if hash == lastGoalHash {
		return Block{Scope: "goal"}, false, nil
	}
	lastGoalHash = hash
	return goalPrewarmUncached(conn, cfg, userPrompt)
}

func goalPrewarmUncached(conn *sql.DB, cfg config.Config, userPrompt string) (Block, bool, error) {
	keywords := extractKeywords(userPrompt)
	if len(keywords) == 0 {
		return Block{Scope: "goal"}, false, nil
	}
	var lines []string
	var qerr error
	for _, kw := range keywords {
		like := "%" + kw + "%"
		rows, err := conn.Query(`
			SELECT category, statement, confidence FROM preferences
			WHERE valid_until IS NULL AND (statement LIKE ? OR category LIKE ?)
			  AND id NOT IN (SELECT conflicting_id FROM conflicts WHERE resolved_at IS NULL)
			ORDER BY confidence DESC LIMIT 5`, like, like)
		if err != nil {
			qerr = err
			continue
		}
		for rows.Next() {
			var cat, stmt string
			var conf float64
			if err := rows.Scan(&cat, &stmt, &conf); err != nil {
				continue
			}
			// Same bullet shape as the session block and TASTE.md, with the
			// category inline since this block has no headings.
			lines = append(lines, fmt.Sprintf("- [%s] %s. Confidence: %.2f", cat, strings.TrimSuffix(stmt, "."), conf))
		}
		rows.Close()
		if len(lines) > 0 {
			break
		}
		_ = qerr
	}
	if len(lines) == 0 {
		return Block{Scope: "goal"}, false, nil
	}
	content := "<palate-prewarm scope=\"goal\">\n" + strings.Join(lines, "\n") + "\n</palate-prewarm>"
	content = truncateToTokens(content, cfg.TokenBudgets.PrewarmGoal)
	return Block{Content: content, Tokens: estimateTokens(content), Scope: "goal"}, true, nil
}

func goalHash(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.Join(strings.Fields(p), " ")
	words := strings.Fields(p)
	if len(words) > 20 {
		words = words[:20]
	}
	return strings.Join(words, " ")
}

func extractKeywords(prompt string) []string {
	words := strings.Fields(strings.ToLower(prompt))
	stop := map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true, "to": true, "for": true, "in": true, "on": true, "of": true, "and": true, "or": true, "please": true, "can": true, "you": true, "i": true, "me": true, "my": true}
	var out []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:\"'()[]{}")
		if len(w) < 3 || stop[w] {
			continue
		}
		out = append(out, w)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func ResetGoalDedup() { lastGoalHash = "" }
