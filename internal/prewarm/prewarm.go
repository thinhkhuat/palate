package prewarm

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
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

func SessionPrewarm(conn *sql.DB, cfg config.Config) (Block, error) {
	rows, err := conn.Query(`
		SELECT category, statement, confidence, importance, trust_tier, provenance, last_verified_at
		FROM preferences
		WHERE valid_until IS NULL
		  AND id NOT IN (SELECT conflicting_id FROM conflicts WHERE resolved_at IS NULL)
		ORDER BY confidence * importance DESC, id ASC
		LIMIT 20`)
	if err != nil {
		return Block{Scope: "session"}, err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var cat, stmt string
		var conf, imp float64
		var trustTier, provenance, lastVerified sql.NullString
		if err := rows.Scan(&cat, &stmt, &conf, &imp, &trustTier, &provenance, &lastVerified); err != nil {
			continue
		}
		badge := ""
		if trustTier.Valid && trustTier.String == "IMPORTED" {
			badge = " [IMPORTED]"
		}
		marker := ""
		if trustTier.Valid && trustTier.String == "DISTILLED" {
			marker = ", distilled"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s (%.2f%s)%s", cat, stmt, conf, marker, badge))
	}
	if len(lines) == 0 {
		return Block{Content: "", Tokens: 0, Scope: "session"}, nil
	}
	content := "<palate-prewarm scope=\"session\">\n" + strings.Join(lines, "\n") + "\n</palate-prewarm>"
	content = truncateToTokens(content, cfg.TokenBudgets.PrewarmSession)
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
			lines = append(lines, fmt.Sprintf("- [%s] %s", cat, stmt))
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
