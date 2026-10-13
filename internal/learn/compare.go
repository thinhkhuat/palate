package learn

import (
	"database/sql"
	"regexp"
	"sort"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

// compareTokenRe extracts candidate FTS query tokens from free text
// (phase-03 req 2): runs of letters or digits, at least 3 code points long.
// Punctuation, FTS5 operator characters ("\"", "*", "-", ":") and short
// words never survive this cut, so they never reach the MATCH query either.
var compareTokenRe = regexp.MustCompile(`[\p{L}\p{N}]{3,}`)

// maxCompareTokens bounds how many distinct tokens feed the FTS query — the
// longest, most specific words in text, not every word in a long batch.
const maxCompareTokens = 32

// ftsQuery builds a `"tok1" OR "tok2" OR ...` FTS5 MATCH expression from the
// longest maxCompareTokens distinct, non-stop-word tokens in text (ties break
// on first occurrence, for determinism). Returns "" when text yields no
// usable token, which CompareSet treats as "nothing to search for".
func ftsQuery(text string) string {
	type tok struct {
		word string
		pos  int
	}
	seen := map[string]bool{}
	var toks []tok
	for i, w := range compareTokenRe.FindAllString(text, -1) {
		lw := strings.ToLower(w)
		if stopWords[lw] || seen[lw] {
			continue
		}
		seen[lw] = true
		toks = append(toks, tok{lw, i})
	}
	sort.SliceStable(toks, func(i, j int) bool {
		if len(toks[i].word) != len(toks[j].word) {
			return len(toks[i].word) > len(toks[j].word)
		}
		return toks[i].pos < toks[j].pos
	})
	if len(toks) > maxCompareTokens {
		toks = toks[:maxCompareTokens]
	}
	if len(toks) == 0 {
		return ""
	}
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = `"` + t.word + `"`
	}
	return strings.Join(parts, " OR ")
}

// CompareSet is what the distiller, repo learning and the judge each compare
// a candidate against, instead of the whole live ledger (phase-03 req 2,
// review F2): the current profile (SelectProfile, so its own eligibility and
// budget rules already apply) plus up to compareTopK live rows outside it,
// ranked by FTS5 bm25 over fts_preferences against tokens drawn from text,
// deduplicated by id and excluding conflict losers. An empty token set or an
// FTS error falls back to the profile alone and bumps compare_fts_error —
// both are "nothing more to add", never a hard failure for the caller.
func CompareSet(conn *sql.DB, cfg config.Config, text string) ([]Preference, error) {
	profile, err := SelectProfile(conn, cfg, cfg.TokenBudgets.PrewarmSession)
	if err != nil {
		return nil, err
	}
	out := append([]Preference(nil), profile.In...)
	inSet := make(map[string]bool, len(out))
	for _, p := range out {
		inSet[p.ID] = true
	}

	query := ftsQuery(text)
	if query == "" {
		storage.BumpDrop(conn, "compare_fts_error")
		return out, nil
	}

	losers, err := conflictLosers(conn)
	if err != nil {
		return nil, err
	}

	rows, err := conn.Query(`SELECT p.id, p.category, p.statement, p.confidence, p.importance, p.source, p.provenance, p.valid_from, p.valid_until, p.created_at, p.last_reinforced_at, p.reinforcement_count, p.owner, p.trust_tier, p.veracity
		FROM fts_preferences f JOIN preferences p ON p.rowid = f.rowid
		WHERE fts_preferences MATCH ? AND p.valid_until IS NULL
		ORDER BY bm25(fts_preferences)
		LIMIT ?`, query, compareTopK)
	if err != nil {
		storage.BumpDrop(conn, "compare_fts_error")
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var p Preference
		var vu sql.NullString
		if err := rows.Scan(&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &vu, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity); err != nil {
			continue
		}
		if vu.Valid {
			p.ValidUntil = &vu.String
		}
		if inSet[p.ID] {
			continue
		}
		if _, isLoser := losers[p.ID]; isLoser {
			continue
		}
		inSet[p.ID] = true
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		storage.BumpDrop(conn, "compare_fts_error")
	}
	return out, nil
}
