package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
)

// noticeCursorFile remembers the last event a notice covered.
const noticeCursorFile = "notice_cursor"

// learnedSinceNotice tells the user what Palate learned since the previous
// notice on this home — Command Code shows the same thing as inline "taste
// learned / upgraded / downgraded" notices while you work. SessionStart and
// UserPromptSubmit both call it (since names the gap: "since your last
// session" / "since your last prompt"). It reads the events past the stored
// cursor, advances the cursor, and returns the one-line notice plus a
// <palate-learned> block listing the rows created or reworded in that span —
// what the model must apply from the next turn on, since the SessionStart
// profile predates them. Both are "" when nothing changed. The first run on a
// home only seeds the cursor: dumping the whole history as "news" would be
// noise.
func learnedSinceNotice(db *sql.DB, cfg config.Config, since string) (notice, learnedBlock string) {
	// Two hooks on one home (two panes, a resume racing a startup) must not
	// both claim the same delta: the cursor is read and advanced under the
	// same lock the distiller uses. A busy lock means another hook is
	// reporting it right now — say nothing rather than repeat it.
	release, ok := distill.AcquireLearnLock(cfg.StatePath("notice.lock"))
	if !ok {
		return "", ""
	}
	defer release()
	var last int64
	_ = db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&last)
	path := cfg.StatePath(noticeCursorFile)
	raw, err := os.ReadFile(path)
	cursor, perr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || perr != nil {
		// Missing or unreadable cursor: seed it. Never dump the whole
		// history as news.
		_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
		return "", ""
	}
	if last <= cursor {
		return "", ""
	}
	rows, err := db.Query(`SELECT event_type, payload FROM events WHERE id > ? ORDER BY id`, cursor)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	var learned, refined, consolidated, retired int
	var newIDs, changedIDs []string
	seen := map[string]bool{}
	for rows.Next() {
		var typ, payload string
		if rows.Scan(&typ, &payload) != nil {
			continue
		}
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(payload), &p)
		switch typ {
		case "create":
			learned++
			if p.ID != "" {
				newIDs = append(newIDs, p.ID)
			}
		case "refine", "reinforce", "correct":
			refined++
			if typ == "refine" && p.ID != "" && !seen[p.ID] {
				// A reworded row is news to the model too.
				seen[p.ID] = true
				changedIDs = append(changedIDs, p.ID)
			}
		case "consolidate_merge", "consolidate_rewrite", "consolidate_rescore":
			// Housekeeping over rows the user already knows — never "new".
			consolidated++
		case "weaken", "weaken_close", "forget", "consolidate_retire":
			retired++
		}
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
	if learned+refined+consolidated+retired == 0 {
		return "", ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "palate: %s — learned %d, refined %d, consolidated %d, retired %d.", since, learned, refined, consolidated, retired)
	shown := 0
	for _, id := range newIDs {
		if shown == 3 {
			break
		}
		var stmt string
		if db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, id).Scan(&stmt) != nil || stmt == "" {
			continue
		}
		if len(stmt) > 90 {
			cut := strings.LastIndex(stmt[:87], " ") // never cut mid-word
			if cut < 40 {
				cut = 87
			}
			stmt = stmt[:cut] + "…"
		}
		if shown == 0 {
			b.WriteString(" New: ")
		} else {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%q", stmt)
		shown++
	}
	fmt.Fprintf(&b, " (full profile: %s)", cfg.TastePath())
	return b.String(), learnedLines(db, cfg, append(newIDs, changedIDs...), since)
}

// seedNoticeCursor writes the cursor at the current event head when no
// cursor exists yet (palate init on a fresh home). An existing cursor is
// left alone.
func seedNoticeCursor(db *sql.DB, cfg config.Config) {
	path := cfg.StatePath(noticeCursorFile)
	if _, err := os.Stat(path); err == nil {
		return
	}
	var last int64
	_ = db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&last)
	_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
}

// learnedLines renders the rows behind ids in the profile's own bullet shape
// inside a <palate-learned> block, or "" when none of them is still active.
func learnedLines(db *sql.DB, cfg config.Config, ids []string, since string) string {
	var lines []string
	for _, id := range ids {
		var cat, stmt string
		var c float64
		if db.QueryRow(`SELECT category, statement, confidence FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&cat, &stmt, &c) != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s Confidence: %.2f", cat, strings.TrimSuffix(stmt, ".")+".", c))
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("<palate-learned>\nPreferences learned or reworded %s — apply them from now on (full profile: %s):\n%s\n</palate-learned>", since, cfg.TastePath(), strings.Join(lines, "\n"))
}
