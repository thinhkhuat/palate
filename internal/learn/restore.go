package learn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thinhkhuat/palate/internal/storage"
)

// Restore reverses whichever exit applies to id (phase-02 requirement 11):
//
//   - a retired row (valid_until set, by forget/consolidate_retire/
//     taste_edit_forget/weaken_close): clear valid_until, bump
//     last_reinforced_at to now, append a "restore" event {"id","was":"retired"}.
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
		if _, err := conn.Exec(`UPDATE preferences SET valid_until=NULL, last_reinforced_at=? WHERE id=?`, now, id); err != nil {
			return false, err
		}
		return true, storage.AppendEvent(conn, "restore", fmt.Sprintf(`{"id":%q,"was":%q}`, id, "retired"))
	}
	if _, err := conn.Exec(`UPDATE preferences SET trust_tier='STATED' WHERE id=?`, id); err != nil {
		return false, err
	}
	if err := dropProjectSpecificTag(conn, id); err != nil {
		return false, err
	}
	return true, storage.AppendEvent(conn, "restore", fmt.Sprintf(`{"id":%q,"was":%q,"from_tier":%q}`, id, "candidate", trustTier))
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
