package learn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/storage"
)

// Restore reverses whichever exit applies to id (phase-02 requirement 11):
//
//   - a retired row (valid_until set, by forget/consolidate_retire/
//     taste_edit_forget/weaken_close): clear valid_until, bump
//     last_reinforced_at to now, append a "restore" event {"id","was":"retired"}.
//     A row the stale-candidate step retired is also promoted to STATED (the
//     candidate rule below), so the next pass cannot forget it again for the
//     same reason; its event also carries "from_tier".
//   - a live row that fell out of the profile (a candidate the dream pass or
//     the budget excluded): set trust_tier='STATED' so it is never excluded
//     for lack of sessions again, drop the project_specific tag so
//     profile.exclude_project_specific stops hiding it, append a "restore"
//     event {"id","was":"candidate","from_tier"}.
//
// Callers back up the store before calling Restore (the same pattern
// storage.Forget's own callers use) — Restore itself only touches the row and
// the events log. found is false when id names no preference at all.
func Restore(conn *sql.DB, id string) (found bool, err error) {
	var validUntil sql.NullString
	var trustTier string
	if err := conn.QueryRow(`SELECT valid_until, trust_tier FROM preferences WHERE id=?`, id).Scan(&validUntil, &trustTier); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("not found: %s", id)
		}
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if validUntil.Valid {
		stale := retiredAsStaleCandidate(conn, id) && trustTier != "STATED"
		if _, err := conn.Exec(`UPDATE preferences SET valid_until=NULL, last_reinforced_at=? WHERE id=?`, now, id); err != nil {
			return false, err
		}
		if !stale {
			if !reopened(conn, id) {
				return true, errStillClosed(id)
			}
			return true, storage.AppendEvent(conn, "restore", fmt.Sprintf(`{"id":%q,"was":%q}`, id, "retired"))
		}
		// Conditional on the row being live: a close that landed after the
		// clear above must leave its tier and tags as they were.
		res, err := conn.Exec(`UPDATE preferences SET trust_tier='STATED' WHERE id=? AND valid_until IS NULL`, id)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return true, errStillClosed(id)
		}
		if err := dropProjectSpecificTag(conn, id); err != nil {
			return false, err
		}
		if !reopened(conn, id) {
			return true, errStillClosed(id)
		}
		return true, storage.AppendEvent(conn, "restore", fmt.Sprintf(`{"id":%q,"was":%q,"from_tier":%q}`, id, "retired", trustTier))
	}
	// Conditional on the row still being live: a merge or retire in another
	// process between the read and this write closes the row, and promoting a
	// closed row would report a restore that did not happen.
	res, err := conn.Exec(`UPDATE preferences SET trust_tier='STATED' WHERE id=? AND valid_until IS NULL`, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return true, errStillClosed(id)
	}
	if err := dropProjectSpecificTag(conn, id); err != nil {
		return false, err
	}
	return true, storage.AppendEvent(conn, "restore", fmt.Sprintf(`{"id":%q,"was":%q,"from_tier":%q}`, id, "candidate", trustTier))
}

func errStillClosed(id string) error {
	return fmt.Errorf("%s is still closed: another process closed it while it was being restored; run `palate restore %s` again", id, id)
}

// reopened reports whether id is live now; the retired branch checks it after
// its writes so a close that landed in between is not reported as a restore.
func reopened(conn *sql.DB, id string) bool {
	var n int
	return conn.QueryRow(`SELECT count(*) FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&n) == nil && n == 1
}

// StaleCandidateReasonPrefix opens the reason of a consolidate_retire event
// written by the dream pass's stale-candidate step.
const StaleCandidateReasonPrefix = "stale_candidate:"

// retiredAsStaleCandidate reports whether the latest event that closed id was
// the stale-candidate step's consolidate_retire.
func retiredAsStaleCandidate(conn *sql.DB, id string) bool {
	var typ, reason string
	err := conn.QueryRow(`SELECT event_type, COALESCE(json_extract(payload,'$.reason'), '') FROM events
		WHERE event_type IN ('consolidate_retire','forget','taste_edit_forget','weaken_close') AND json_extract(payload,'$.id')=?
		ORDER BY id DESC LIMIT 1`, id).Scan(&typ, &reason)
	return err == nil && typ == "consolidate_retire" && strings.HasPrefix(reason, StaleCandidateReasonPrefix)
}

// dropProjectSpecificTag removes TagProjectSpecific from id's provenance
// array, leaving every other tag (merged_from, rulebook_overlap, taste_edit,
// the signal type, …) untouched. A no-op when the tag is absent.
func dropProjectSpecificTag(conn *sql.DB, id string) error {
	var prov string
	if err := conn.QueryRow(`SELECT provenance FROM preferences WHERE id=?`, id).Scan(&prov); err != nil {
		return err
	}
	if !HasProjectSpecific(prov) {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(prov), &tags); err != nil {
		// Malformed provenance is not this function's problem to fix; leave
		// it alone rather than risk destroying data with a partial rewrite.
		return nil
	}
	kept := make([]string, 0, len(tags))
	for _, tg := range tags {
		if tg != TagProjectSpecific {
			kept = append(kept, tg)
		}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	_, err = conn.Exec(`UPDATE preferences SET provenance=? WHERE id=?`, string(b), id)
	return err
}
