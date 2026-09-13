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

// noticeCursorFile remembers the last event a SessionStart notice covered.
const noticeCursorFile = "notice_cursor"

// learnedSinceNotice tells the user what Palate learned since the previous
// SessionStart on this home — Command Code shows the same thing as inline
// "taste learned / upgraded / downgraded" notices while you work. It reads
// the events past the stored cursor, advances the cursor, and returns "" when
// nothing changed. The first run on a home only seeds the cursor: dumping the
// whole history as "news" would be noise.
func learnedSinceNotice(db *sql.DB, cfg config.Config) string {
	// Two SessionStarts on one home (two panes, a resume racing a startup)
	// must not both claim the same delta: the cursor is read and advanced
	// under the same lock the distiller uses. A busy lock means another
	// hook is reporting it right now — say nothing rather than repeat it.
	release, ok := distill.AcquireLearnLock(cfg.StatePath("notice.lock"))
	if !ok {
		return ""
	}
	defer release()
	var last int64
	_ = db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&last)
	path := cfg.StatePath(noticeCursorFile)
	raw, err := os.ReadFile(path)
	cursor, perr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || perr != nil {
		// Missing or unreadable cursor: seed it. Never dump the whole
		// history as "since your last session".
		_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
		return ""
	}
	if last <= cursor {
		return ""
	}
	rows, err := db.Query(`SELECT event_type, payload FROM events WHERE id > ? ORDER BY id`, cursor)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var learned, refined, consolidated, retired int
	var newIDs []string
	for rows.Next() {
		var typ, payload string
		if rows.Scan(&typ, &payload) != nil {
			continue
		}
		switch typ {
		case "create":
			learned++
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(payload), &p)
			if p.ID != "" {
				newIDs = append(newIDs, p.ID)
			}
		case "refine", "reinforce", "correct":
			refined++
		case "consolidate_merge", "consolidate_rewrite", "consolidate_rescore":
			// Housekeeping over rows the user already knows — never "new".
			consolidated++
		case "weaken", "weaken_close", "forget", "consolidate_retire":
			retired++
		}
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
	if learned+refined+consolidated+retired == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "palate: since your last session — learned %d, refined %d, consolidated %d, retired %d.", learned, refined, consolidated, retired)
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
	return b.String()
}
