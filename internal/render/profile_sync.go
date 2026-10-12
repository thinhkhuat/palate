package render

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// retireEventTypes is, in the order requirement 3.3 names them, every event
// type that can explain why a once-live row is no longer live: the latest
// one among them names the cut reason for a row that left the profile
// because it left the ledger, not because it lost the budget.
var retireEventTypes = []string{"consolidate_retire", "consolidate_merge", "forget", "taste_edit_forget", "weaken_close"}

// syncProfileEvents is Sync's own diff-and-events step (req 3.2/3.3): it
// selects the current profile, compares it against the previous membership
// (learn.Membership — the latest profile_add/profile_cut per id, or, on the
// very first run, the ids matching bullets already on disk), and appends
// profile_add/profile_cut events for exactly what changed. Called only from
// inside Sync's own render lock; it never runs standalone.
func syncProfileEvents(conn *sql.DB, cfg config.Config) error {
	profile, err := learn.SelectProfile(conn, cfg, ProfileBudget(cfg))
	if err != nil {
		return err
	}
	membership, err := learn.Membership(conn)
	if err != nil {
		return err
	}
	seeded := len(membership) == 0
	var prevMembers map[string]bool
	if seeded {
		prevMembers, err = seedMembershipFromDisk(conn, cfg)
		if err != nil {
			return err
		}
	} else {
		prevMembers = map[string]bool{}
		for id, evt := range membership {
			if evt.EventType == "profile_add" {
				prevMembers[id] = true
			}
		}
	}

	nowMembers := map[string]bool{}
	byID := map[string]learn.Preference{}
	for _, p := range profile.In {
		nowMembers[p.ID] = true
		byID[p.ID] = p
	}

	for id := range nowMembers {
		if prevMembers[id] {
			continue
		}
		reason := addReason(byID[id])
		if err := storage.AppendEvent(conn, "profile_add", fmt.Sprintf(`{"id":%q,"reason":%q}`, id, reason)); err != nil {
			return err
		}
	}

	sessionCounts, err := learn.SessionCounts(conn)
	if err != nil {
		return err
	}
	for id := range prevMembers {
		if nowMembers[id] {
			continue
		}
		reason := cutReason(conn, profile, id)
		payload := map[string]any{"id": id, "reason": reason, "sessions": sessionCounts[id]}
		if seeded {
			payload["seed"] = true
		}
		if sup := supersededBy(conn, id); sup != "" {
			payload["superseded_by"] = sup
		}
		b, merr := json.Marshal(payload)
		if merr != nil {
			return merr
		}
		if err := storage.AppendEvent(conn, "profile_cut", string(b)); err != nil {
			return err
		}
	}
	return nil
}

// addReason is a short, human-readable account of why a row cleared
// eligibility, shown by the notice's "New in profile:" list.
func addReason(p learn.Preference) string {
	if p.TrustTier == "STATED" {
		return "STATED"
	}
	return fmt.Sprintf("%s, seen again", p.TrustTier)
}

// cutReason names why id left the profile: Profile.Excluded's reason when it
// is still live (over budget, needs another session, project-specific, a
// rulebook restatement, or a conflict loser), or, when it is no longer live
// at all, the reason on the latest retiring event named in retireEventTypes.
func cutReason(conn *sql.DB, profile learn.Profile, id string) string {
	if r, ok := profile.Excluded[id]; ok {
		return r
	}
	typ, reason := lastRetireEvent(conn, id)
	if reason != "" {
		return reason
	}
	if typ != "" {
		return typ
	}
	return "no longer live"
}

// lastRetireEvent returns the most recent retireEventTypes event for id —
// its type, and its own payload.reason when it carries one.
func lastRetireEvent(conn *sql.DB, id string) (eventType, reason string) {
	placeholders := make([]string, len(retireEventTypes))
	args := make([]any, 0, len(retireEventTypes)+1)
	for i, t := range retireEventTypes {
		placeholders[i] = "?"
		args = append(args, t)
	}
	args = append(args, id)
	q := `SELECT event_type, payload FROM events WHERE event_type IN (` + strings.Join(placeholders, ",") + `)
		AND json_extract(payload,'$.id')=? ORDER BY id DESC LIMIT 1`
	var payload string
	if err := conn.QueryRow(q, args...).Scan(&eventType, &payload); err != nil {
		return "", ""
	}
	var p struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
	return eventType, p.Reason
}

// supersededBy returns the id of the live row whose provenance carries
// "merged_from:[...]" naming id — the survivor a merge folded id into — or
// "" when no live row names it. provenance is a JSON array of strings, and
// merged_from's own value is itself JSON text riding inside one of those
// strings (the same two-level encoding learn's own loadMergeMap parses), so
// a plain substring search would need to match against backslash-escaped
// quotes; parsing both levels properly avoids that fragility.
func supersededBy(conn *sql.DB, id string) string {
	rows, err := conn.Query(`SELECT id, provenance FROM preferences WHERE valid_until IS NULL AND provenance LIKE '%merged_from:%'`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var keepID, prov string
		if rows.Scan(&keepID, &prov) != nil {
			continue
		}
		var tags []string
		if json.Unmarshal([]byte(prov), &tags) != nil {
			continue
		}
		for _, tag := range tags {
			rest, ok := strings.CutPrefix(tag, "merged_from:")
			if !ok {
				continue
			}
			var mergedIDs []string
			if json.Unmarshal([]byte(rest), &mergedIDs) != nil {
				continue
			}
			for _, mid := range mergedIDs {
				if mid == id {
					return keepID
				}
			}
		}
	}
	return ""
}

// seedMembershipFromDisk is the first-run fallback (req 3.2): with no
// profile_add/profile_cut event yet anywhere in the store, the previous
// membership is the set of live ids whose statement matches a bullet already
// on disk at cfg.TastePath(). A missing or empty file seeds an empty
// membership (every currently-selected row is then a fresh profile_add,
// which is correct: nothing was ever recorded as a member before).
func seedMembershipFromDisk(conn *sql.DB, cfg config.Config) (map[string]bool, error) {
	out := map[string]bool{}
	b, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		return out, nil
	}
	stmts := map[string]bool{}
	for _, e := range bulletEntries(extractBody(string(b))) {
		stmts[e.stmt] = true
	}
	if len(stmts) == 0 {
		return out, nil
	}
	rows, err := conn.Query(`SELECT id, statement FROM preferences WHERE valid_until IS NULL`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, stmt string
		if rows.Scan(&id, &stmt) != nil {
			continue
		}
		if stmts[normStatement(stmt)] {
			out[id] = true
		}
	}
	return out, rows.Err()
}
