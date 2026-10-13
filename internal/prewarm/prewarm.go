package prewarm

import (
	"database/sql"
	"fmt"
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

// truncateToTokens keeps whole lines only, dropping from the end (before the
// closing tag) until the content fits — never a rune-boundary slice, which
// the old raw-byte cut could break mid multi-byte character (req 9).
func truncateToTokens(s string, budget int) string {
	if estimateTokens(s) <= budget {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= 2 {
		return s
	}
	head, tail := lines[0], lines[len(lines)-1]
	body := lines[1 : len(lines)-1]
	for len(body) > 0 {
		body = body[:len(body)-1]
		candidate := strings.Join(append(append([]string{head}, body...), tail), "\n")
		if estimateTokens(candidate) <= budget {
			return candidate
		}
	}
	return head + "\n" + tail
}

// SessionPrewarm injects the whole active profile — the same bytes as
// TASTE.md's body, budgeted by learn.SelectProfile — so the agent sees
// exactly what the owner sees, no ranking cut applied here (Command Code
// parity: its <taste> section is the complete file). SessionPrewarm has no
// fitting of its own and no "N shown" variant: the budget lives entirely in
// selection (render.Body / learn.SelectProfile), so it can never disagree
// with what TASTE.md itself carries.
func SessionPrewarm(conn *sql.DB, cfg config.Config) (Block, error) {
	body, n, err := render.Body(conn, cfg, render.RenderOptions{})
	if err != nil {
		return Block{Scope: "session"}, err
	}
	if n == 0 {
		// Command Code tells the agent the same thing when its taste file is
		// empty: the feature exists and fills itself in.
		content := fmt.Sprintf("<palate-prewarm scope=\"session\">\nNo preferences learned yet. Palate learns them automatically from your sessions (the Stop hook distills each turn); to record one now, call palate_prefer / palate_remember or run `palate prefer \"…\"`. Full profile, once there is one: %s\n</palate-prewarm>", cfg.TastePath())
		return Block{Content: content, Tokens: estimateTokens(content), Scope: "session"}, nil
	}
	content := render.SessionHeader(cfg, n) + body + render.SessionTail()
	return Block{Content: content, Tokens: estimateTokens(content), Scope: "session"}, nil
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
	// The goal block is off by default (token_budgets.prewarm_goal: 0, §6.6);
	// any positive value turns it back on (req 9).
	if cfg.TokenBudgets.PrewarmGoal <= 0 {
		return Block{Scope: "goal"}, false, nil
	}
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
