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
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/render"
)

// noticeCursorFile remembers the last event a notice covered.
const noticeCursorFile = "notice_cursor"

// learnedSinceNotice tells the user what Palate learned since the previous
// notice on this home — Command Code shows the same thing as inline "taste
// learned / upgraded / downgraded" notices while you work. SessionStart and
// UserPromptSubmit both call it (since names the gap: "since your last
// session" / "since your last prompt"). It reads the events past the stored
// cursor, advances the cursor, and returns the one-line notice plus a
// <palate-learned> block listing the rows that entered the profile in that
// span (or, with profile.prompt_block off, every row created or reworded) —
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
		// A pending hold produces no new events of its own (it applies
		// nothing), so it must be surfaced here too — otherwise a hold with
		// no other ledger activity since the cursor would never be reported.
		if render.HoldPending(cfg) {
			return fmt.Sprintf("palate: %s — a hand edit to TASTE.md is held — run `palate taste --accept-edits` to apply it or `palate taste` to discard it.", since), ""
		}
		return "", ""
	}
	rows, err := db.Query(`SELECT event_type, payload FROM events WHERE id > ? ORDER BY id`, cursor)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	var learned, refined, consolidated, retired, added, cut, refused int
	var createRefineIDs, profileAddIDs, profileCutIDs []string
	leftReasons := map[string]string{}
	seenRefine := map[string]bool{}
	for rows.Next() {
		var typ, payload string
		if rows.Scan(&typ, &payload) != nil {
			continue
		}
		var p struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(payload), &p)
		switch typ {
		case "create":
			learned++
			if p.ID != "" {
				createRefineIDs = append(createRefineIDs, p.ID)
			}
		case "refine", "reinforce", "correct":
			refined++
			if typ == "refine" && p.ID != "" && !seenRefine[p.ID] {
				// A reworded row is news to the model too.
				seenRefine[p.ID] = true
				createRefineIDs = append(createRefineIDs, p.ID)
			}
		case "consolidate_merge", "consolidate_rewrite", "consolidate_rescore":
			// Housekeeping over rows the user already knows — never "new".
			consolidated++
		case "weaken", "weaken_close", "forget", "consolidate_retire":
			retired++
		case "profile_add":
			added++
			if p.ID != "" {
				profileAddIDs = append(profileAddIDs, p.ID)
			}
		case "profile_cut":
			cut++
			if p.ID != "" {
				profileCutIDs = append(profileCutIDs, p.ID)
				leftReasons[p.ID] = p.Reason
			}
		case "taste_edit_refused":
			// A hand-edited line the safety scanner flagged — counted and
			// reported here, never silently stored (Security).
			refused++
		}
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(last, 10)), 0o600)
	if learned+refined+consolidated+retired+added+cut+refused == 0 {
		return "", ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "palate: %s — profile +%d −%d; ledger: learned %d, refined %d, consolidated %d, retired %d.", since, added, cut, learned, refined, consolidated, retired)
	appendStatementList(&b, db, "New in profile", profileAddIDs, nil)
	appendStatementList(&b, db, "Left", profileCutIDs, leftReasons)
	if refused > 0 {
		fmt.Fprintf(&b, " %d hand-edited line(s) refused (unsafe content, never stored).", refused)
	}
	if render.HoldPending(cfg) {
		b.WriteString(" A hand edit to TASTE.md is held — run `palate taste --accept-edits` to apply it or `palate taste` to discard it.")
	}
	learnedIDs := profileAddIDs
	if !cfg.Profile.PromptBlock {
		learnedIDs = createRefineIDs
	}
	return b.String(), learnedLines(db, cfg, learnedIDs, since)
}

// appendStatementList appends " Label: "quote"; "quote"." to b for up to 3
// ids' statements, each with its reasons[id] parenthesized when reasons is
// non-nil and carries one. A row that no longer resolves (a rare race, not
// tested) is skipped rather than shown blank.
func appendStatementList(b *strings.Builder, db *sql.DB, label string, ids []string, reasons map[string]string) {
	shown := 0
	for _, id := range ids {
		if shown == 3 {
			break
		}
		var stmt string
		if db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, id).Scan(&stmt) != nil || stmt == "" {
			continue
		}
		stmt = truncateStatement(stmt)
		if shown == 0 {
			fmt.Fprintf(b, " %s: ", label)
		} else {
			b.WriteString("; ")
		}
		fmt.Fprintf(b, "%q", stmt)
		if reasons != nil {
			if r := reasons[id]; r != "" {
				fmt.Fprintf(b, " (%s)", r)
			}
		}
		shown++
	}
	if shown > 0 {
		b.WriteString(".")
	}
}

// truncateStatement never cuts mid-word: the old 0.3.x cut landed mid-sentence.
func truncateStatement(stmt string) string {
	if len(stmt) <= 90 {
		return stmt
	}
	cut := strings.LastIndex(stmt[:87], " ")
	if cut < 40 {
		cut = 87
	}
	return stmt[:cut] + "…"
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
		// Rulebook-tagged rows are suppressed from TASTE.md; announcing them
		// as "learned" would only confuse — skip them here too. The tag is
		// matched as a quoted JSON string inside the provenance array.
		q := fmt.Sprintf(`SELECT category, statement, confidence FROM preferences
			WHERE id=? AND valid_until IS NULL AND provenance NOT LIKE '%%"%s"%%'`, learn.RulebookOverlap)
		if db.QueryRow(q, id).Scan(&cat, &stmt, &c) != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s Confidence: %.2f", cat, strings.TrimSuffix(stmt, ".")+".", c))
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("<palate-learned>\nPreferences learned or reworded %s — apply them from now on (full profile: %s):\n%s\n</palate-learned>", since, cfg.TastePath(), strings.Join(lines, "\n"))
}
