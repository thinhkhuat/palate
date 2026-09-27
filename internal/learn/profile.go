package learn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
)

// TagProjectSpecific marks a preference row whose statement only makes sense
// for the project it was learned in (a path, a repo-specific tool, a
// project's own naming convention) — never a portable taste. It rides the
// same provenance JSON array RulebookOverlap and merged_from use, so no
// schema change is needed.
const TagProjectSpecific = "project_specific"

// TagTasteEdit marks a preference row created or reinforced by the owner's
// own hand edit to TASTE.md (read back by Phase 2's ReadBack), kept in
// provenance for audit. It carries no special handling of its own here: the
// owner-edit path (Phase 2) reuses SignalExplicitPrefer at judged confidence
// 0.95 and stamps this tag, nothing more.
const TagTasteEdit = "taste_edit"

// HasProjectSpecific reports whether provenance carries TagProjectSpecific,
// the same way HasRulebookOverlap reads RulebookOverlap.
func HasProjectSpecific(provenance string) bool {
	return strings.Contains(provenance, `"`+TagProjectSpecific+`"`)
}

// minSessions is the design's "seen again in a later session" eligibility
// floor for a non-STATED row: two distinct sessions. Not a config key —
// red-team M4 and the risk table treat it as a code-level lever the owner
// does not need to reverse, not an owner default.
const minSessions = 2

// compareTopK is the number of FTS-ranked rows outside the profile that
// Phase 3's compare set adds alongside the profile itself. Not a config key.
const compareTopK = 20

// EstimateTokens is the one token heuristic the profile budget and the
// SessionStart prewarm share (the same ~4-bytes-per-token count
// prewarm.estimateTokens used; Phase 2 switches prewarm to call this
// instead). It is approximate, and more so for Vietnamese text — callers
// should not treat it as exact.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// BulletLine renders one preference the same way render.Body does —
// "- statement. Confidence: 0.xx" with the trailing "." trimmed — so the
// budget is charged against the exact bytes the reader sees
// (render/taste_md.go:113).
func BulletLine(p Preference) string {
	return fmt.Sprintf("- %s. Confidence: %.2f", strings.TrimSuffix(p.Statement, "."), p.Confidence)
}

// MembershipEvent is the latest profile_add/profile_cut event recorded for a
// preference id (Phase 2's render.Sync is the only writer). Sessions and Seed
// are only meaningful when EventType is "profile_cut": Sessions is the
// distinct-session count recorded at cut time, and Seed marks a cut written
// by Phase 2's first-run seed, which competes on evidence like any other row
// instead of being demoted as stale-cut (red-team M3).
type MembershipEvent struct {
	EventType string
	Seed      bool
	Sessions  int
}

// Membership reads the latest profile_add/profile_cut event per id straight
// from the events table — no state file is shared across processes for
// selection. An id with no membership event yet (never rendered) is simply
// absent from the map.
func Membership(conn *sql.DB) (map[string]MembershipEvent, error) {
	rows, err := conn.Query(`SELECT json_extract(payload,'$.id'), event_type, payload FROM events
		WHERE event_type IN ('profile_add','profile_cut') ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MembershipEvent{}
	for rows.Next() {
		var id, typ, payload string
		if err := rows.Scan(&id, &typ, &payload); err != nil {
			continue
		}
		if id == "" {
			continue
		}
		var raw struct {
			Sessions int  `json:"sessions"`
			Seed     bool `json:"seed"`
		}
		_ = json.Unmarshal([]byte(payload), &raw)
		// Rows are visited in ascending event id order, so the last write
		// for this id wins — always the latest membership event.
		out[id] = MembershipEvent{EventType: typ, Seed: raw.Seed, Sessions: raw.Sessions}
	}
	return out, rows.Err()
}

// SessionCounts returns, per live preference id, the number of distinct
// sessions backing its create/reinforce history. Each event's identity is its
// payload.session when present, otherwise "day:"+its created_at's UTC
// calendar day (a proxy for legacy events written before Phase 3 carried a
// session key). A real session key and a day-proxy key that land on the same
// UTC day count as one session — the switch-over day must not double-count a
// legacy row the new binary reinforces once (red-team M4) — but two distinct
// real session keys on the same day still count as two, since a real key
// already identifies the session precisely. merged_from provenance tags are
// expanded recursively (including retired rows) with a cycle guard, so a
// merge survivor's count unions every session that ever reinforced any row
// folded into it.
func SessionCounts(conn *sql.DB) (map[string]int, error) {
	type sessionEvent struct {
		session string
		day     string
	}
	evByID := map[string][]sessionEvent{}
	rows, err := conn.Query(`SELECT json_extract(payload,'$.id'), COALESCE(json_extract(payload,'$.session'), ''), substr(created_at,1,10)
		FROM events WHERE event_type IN ('create','reinforce')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, session, day string
		if err := rows.Scan(&id, &session, &day); err != nil {
			continue
		}
		if id == "" {
			continue
		}
		evByID[id] = append(evByID[id], sessionEvent{session: session, day: day})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mergeMap, err := loadMergeMap(conn)
	if err != nil {
		return nil, err
	}
	liveIDs, err := livePreferenceIDs(conn)
	if err != nil {
		return nil, err
	}

	out := make(map[string]int, len(liveIDs))
	for _, id := range liveIDs {
		days := map[string]bool{}
		keyDays := map[string]map[string]bool{}
		for _, gid := range expandMergeGroup(id, mergeMap) {
			for _, e := range evByID[gid] {
				if e.session == "" {
					days[e.day] = true
					continue
				}
				if keyDays[e.session] == nil {
					keyDays[e.session] = map[string]bool{}
				}
				keyDays[e.session][e.day] = true
			}
		}
		count := len(days)
		for _, onDays := range keyDays {
			overlapsLegacyDay := false
			for d := range onDays {
				if days[d] {
					overlapsLegacyDay = true
					break
				}
			}
			if !overlapsLegacyDay {
				count++
			}
		}
		out[id] = count
	}
	return out, nil
}

// loadMergeMap returns, for every preference row (live or retired) whose
// provenance carries a "merged_from:[…]" tag (written by consolidate's merge
// action), the ids it names.
func loadMergeMap(conn *sql.DB) (map[string][]string, error) {
	rows, err := conn.Query(`SELECT id, provenance FROM preferences WHERE provenance LIKE '%merged_from:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, prov string
		if err := rows.Scan(&id, &prov); err != nil {
			continue
		}
		var tags []string
		if err := json.Unmarshal([]byte(prov), &tags); err != nil {
			continue
		}
		for _, tag := range tags {
			rest, ok := strings.CutPrefix(tag, "merged_from:")
			if !ok {
				continue
			}
			var ids []string
			if err := json.Unmarshal([]byte(rest), &ids); err == nil {
				out[id] = append(out[id], ids...)
			}
		}
	}
	return out, rows.Err()
}

// expandMergeGroup walks mergeMap from root, following merged_from edges
// recursively, and returns every id reached (root included). A visited set
// guards against a cycle in malformed provenance.
func expandMergeGroup(root string, mergeMap map[string][]string) []string {
	visited := map[string]bool{}
	var out []string
	var walk func(id string)
	walk = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		out = append(out, id)
		for _, sub := range mergeMap[id] {
			walk(sub)
		}
	}
	walk(root)
	return out
}

func livePreferenceIDs(conn *sql.DB) ([]string, error) {
	rows, err := conn.Query(`SELECT id FROM preferences WHERE valid_until IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// conflictLosers returns, for every live row that is the strictly
// lower-confidence side of an open conflict, the id of the side that beat it.
// It mirrors ListPreferences's two exclusion subqueries (preferences.go:425-
// 426) exactly, so a row this reports as a loser is exactly one
// ListPreferences(conn,false) already hides — an equal-confidence conflict
// names no loser on either side, kept eligible deliberately (red-team L2).
func conflictLosers(conn *sql.DB) (map[string]string, error) {
	rows, err := conn.Query(`
		SELECT c.conflicting_id, c.preference_id FROM conflicts c
			JOIN preferences loser ON loser.id=c.conflicting_id
			JOIN preferences winner ON winner.id=c.preference_id
			WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence
		UNION ALL
		SELECT c.preference_id, c.conflicting_id FROM conflicts c
			JOIN preferences loser ON loser.id=c.preference_id
			JOIN preferences winner ON winner.id=c.conflicting_id
			WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var loser, winner string
		if err := rows.Scan(&loser, &winner); err != nil {
			continue
		}
		out[loser] = winner
	}
	return out, rows.Err()
}

// Profile is what SelectProfile returns: the rows that made the cut, every
// live row that did not with the reason it was left out, and the token cost
// of In (never more than the caller's budget).
type Profile struct {
	In       []Preference
	Excluded map[string]string // id -> reason
	Tokens   int
}

// candidateRow is an eligible row plus the derived facts SelectProfile ranks
// it by.
type candidateRow struct {
	pref     Preference
	sessions int
	staleCut bool
	member   bool
}

// SelectProfile turns the live ledger into a deterministic, token-budgeted
// profile (phase-01 requirements 3-6). It only reads: membership itself is
// written by Phase 2's locked render.Sync, never here.
func SelectProfile(conn *sql.DB, cfg config.Config, budget int) (Profile, error) {
	all, err := allLivePreferences(conn)
	if err != nil {
		return Profile{}, err
	}
	losers, err := conflictLosers(conn)
	if err != nil {
		return Profile{}, err
	}
	sessions, err := SessionCounts(conn)
	if err != nil {
		return Profile{}, err
	}
	membership, err := Membership(conn)
	if err != nil {
		return Profile{}, err
	}

	excluded := map[string]string{}
	var eligible []candidateRow
	for _, p := range all {
		if HasRulebookOverlap(p.Provenance) {
			excluded[p.ID] = "restates rulebook"
			continue
		}
		if winner, isLoser := losers[p.ID]; isLoser {
			excluded[p.ID] = "conflict loser to " + winner
			continue
		}
		if cfg.Profile.ExcludeProjectSpecific && HasProjectSpecific(p.Provenance) {
			excluded[p.ID] = "project-specific"
			continue
		}
		n := sessions[p.ID]
		if p.TrustTier != "STATED" && n < minSessions {
			word := "session"
			if n != 1 {
				word = "sessions"
			}
			excluded[p.ID] = fmt.Sprintf("%d %s (needs %d)", n, word, minSessions)
			continue
		}
		evt, hasEvt := membership[p.ID]
		member := hasEvt && evt.EventType == "profile_add"
		staleCut := hasEvt && evt.EventType == "profile_cut" && !evt.Seed && n <= evt.Sessions
		eligible = append(eligible, candidateRow{pref: p, sessions: n, staleCut: staleCut, member: member})
	}

	sortCandidates(eligible)

	used := 0
	headed := map[string]bool{}
	var in []Preference
	for i, c := range eligible {
		cost := EstimateTokens(BulletLine(c.pref)) + 1
		headCost := 0
		if !headed[c.pref.Category] {
			headCost = EstimateTokens("## "+c.pref.Category) + 2
		}
		total := cost + headCost
		if used+total > budget {
			excluded[c.pref.ID] = fmt.Sprintf("over budget (rank %d)", i+1)
			continue
		}
		used += total
		headed[c.pref.Category] = true
		in = append(in, c.pref)
	}

	return Profile{In: in, Excluded: excluded, Tokens: used}, nil
}

// sortCandidates applies the one deterministic rank key (phase-01 req 4):
// STATED first; then not-stale-cut; then distinct sessions desc; then
// confidence desc; then last_reinforced_at desc; then current member before
// non-member; then shorter statement; then id ascending as the final,
// unconditional tie-break.
func sortCandidates(rows []candidateRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if aStated, bStated := a.pref.TrustTier == "STATED", b.pref.TrustTier == "STATED"; aStated != bStated {
			return aStated
		}
		if a.staleCut != b.staleCut {
			return !a.staleCut
		}
		if a.sessions != b.sessions {
			return a.sessions > b.sessions
		}
		if a.pref.Confidence != b.pref.Confidence {
			return a.pref.Confidence > b.pref.Confidence
		}
		if a.pref.LastReinforcedAt != b.pref.LastReinforcedAt {
			return a.pref.LastReinforcedAt > b.pref.LastReinforcedAt
		}
		if a.member != b.member {
			return a.member
		}
		la, lb := len([]rune(a.pref.Statement)), len([]rune(b.pref.Statement))
		if la != lb {
			return la < lb
		}
		return a.pref.ID < b.pref.ID
	})
}

func allLivePreferences(conn *sql.DB) ([]Preference, error) {
	rows, err := conn.Query(`SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE valid_until IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preference
	for rows.Next() {
		var p Preference
		var vu sql.NullString
		if err := rows.Scan(&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &vu, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity); err != nil {
			continue
		}
		if vu.Valid {
			p.ValidUntil = &vu.String
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
