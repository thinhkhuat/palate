package learn

import (
	"database/sql"
	"strings"
	"unicode"
)

// RewriteAllowed decides whether an existing preference row's stored text may
// be replaced with newText. It gates the text itself, never reinforcement: a
// refused rewrite still lets the caller bump confidence and keep the old
// statement. Both the judge's "refines" path (preferences.go) and the dream
// pass's consolidate_rewrite action (Phase 4) go through here.
//
// Refused when:
//   - p is STATED — the owner's own words, changed only by the owner;
//   - the old statement carries a non-ASCII letter and newText does not (a
//     rewrite must not silently anglicize what was written in another
//     script);
//   - newText is longer, in runes, than the old statement (a rewrite trims;
//     growth without new evidence is the model inventing detail);
//   - requireNewEvidence is set and no create/reinforce event for p.ID is
//     newer (by event id, which is monotonic insertion order) than its own
//     last consolidate_rewrite/refine event — nothing happened since the
//     text last moved.
func RewriteAllowed(conn *sql.DB, p Preference, newText string, requireNewEvidence bool) (bool, string) {
	if p.TrustTier == "STATED" {
		return false, "STATED text is owner-authored and is never rewritten"
	}
	if hasNonASCIILetter(p.Statement) && !hasNonASCIILetter(newText) {
		return false, "the original carries non-ASCII script; the rewrite must keep it"
	}
	if len([]rune(newText)) > len([]rune(p.Statement)) {
		return false, "a rewrite must not be longer than the original"
	}
	if requireNewEvidence {
		lastRewrite := lastEventID(conn, p.ID, "consolidate_rewrite", "refine")
		lastEvidence := lastEventID(conn, p.ID, "create", "reinforce")
		if lastEvidence == 0 || lastEvidence <= lastRewrite {
			return false, "no new evidence since the last rewrite"
		}
	}
	return true, ""
}

func hasNonASCIILetter(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// lastEventID returns the highest events.id (insertion order, immune to
// same-second created_at collisions) among the given event types whose
// payload.id matches id, or 0 when there is none.
func lastEventID(conn *sql.DB, id string, eventTypes ...string) int64 {
	placeholders := make([]string, len(eventTypes))
	args := make([]any, 0, len(eventTypes)+1)
	for i, et := range eventTypes {
		placeholders[i] = "?"
		args = append(args, et)
	}
	args = append(args, id)
	q := `SELECT COALESCE(MAX(id), 0) FROM events WHERE event_type IN (` + strings.Join(placeholders, ",") + `) AND json_extract(payload,'$.id')=?`
	var maxID int64
	_ = conn.QueryRow(q, args...).Scan(&maxID)
	return maxID
}
