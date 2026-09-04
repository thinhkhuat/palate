package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Memory struct {
	ID        string  `json:"id"`
	Content   string  `json:"content"`
	Importance float64 `json:"importance"`
	CreatedAt string  `json:"created_at"`
	SessionID string  `json:"session_id"`
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func Remember(conn *sql.DB, content, sessionID string, importance float64, table string) (string, error) {
	if table != "working_memory" && table != "episodic_memory" && table != "scratchpad" {
		table = "working_memory"
	}
	id := newID()
	now := time.Now().UTC().Format(time.RFC3339)
	var q string
	switch table {
	case "scratchpad":
		q = `INSERT INTO scratchpad(id, content, created_at) VALUES(?,?,?)`
		_, err := conn.Exec(q, id, content, now)
		if err != nil {
			return "", err
		}
	default:
		q = fmt.Sprintf(`INSERT INTO %s(id, content, importance, created_at, session_id) VALUES(?,?,?,?,?)`, table)
		_, err := conn.Exec(q, id, content, importance, now, sessionID)
		if err != nil {
			return "", err
		}
	}
	_ = AppendEvent(conn, "remember", fmt.Sprintf(`{"id":%q,"table":%q}`, id, table))
	return id, nil
}

func Forget(conn *sql.DB, id string) error {
	tables := []string{"working_memory", "episodic_memory", "scratchpad"}
	for _, t := range tables {
		res, _ := conn.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id=?`, t), id)
		if n, _ := res.RowsAffected(); n > 0 {
			_, _ = conn.Exec(`DELETE FROM memory_embeddings WHERE memory_id=?`, id)
			_ = AppendEvent(conn, "forget", fmt.Sprintf(`{"id":%q,"table":%q}`, id, t))
			return nil
		}
	}
	return fmt.Errorf("not found: %s", id)
}

type RecallResult struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
	Table      string  `json:"table"`
	Importance float64 `json:"importance"`
}

func Recall(conn *sql.DB, query string, limit int, includeConflicts bool) ([]RecallResult, error) {
	return RecallAt(conn, query, limit, includeConflicts, nil)
}

func RecallAt(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time) ([]RecallResult, error) {
	return RecallAtWithDecay(conn, query, limit, includeConflicts, asOf, 0, 0)
}

func RecallAtWithDecay(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time, cFloor, halfLifeHours float64) ([]RecallResult, error) {
	return RecallFull(conn, query, limit, includeConflicts, asOf, RecallOptions{CFloor: cFloor, HalfLifeHours: halfLifeHours})
}

type RecallOptions struct {
	CFloor         float64
	HalfLifeHours  float64
	Freshness      FreshnessWeights
	RRFK           int
}

type FreshnessWeights struct {
	Enabled       bool
	T1Days        int
	T1Weight      float64
	T2Days        int
	T2Weight      float64
	T3Weight      float64
}

func RecallFull(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time, opts RecallOptions) ([]RecallResult, error) {
	if limit <= 0 {
		limit = 10
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return recallRecent(conn, limit)
	}
	ftsResults, _ := ftsSearch(conn, query, limit*2)
	prefs, _ := recallPreferencesFull(conn, query, limit*2, includeConflicts, asOf, opts)
	sort.SliceStable(prefs, func(i, j int) bool { return prefs[i].Score > prefs[j].Score })
	k := opts.RRFK
	if k <= 0 {
		k = 60
	}
	merged := rrfMerge(ftsResults, prefs, k)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

func recallRecent(conn *sql.DB, limit int) ([]RecallResult, error) {
	rows, err := conn.Query(`SELECT id, content, importance FROM working_memory ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecallResult
	for rows.Next() {
		var r RecallResult
		if err := rows.Scan(&r.ID, &r.Content, &r.Importance); err != nil {
			continue
		}
		r.Table = "working_memory"
		out = append(out, r)
	}
	return out, nil
}

func ftsSearch(conn *sql.DB, query string, limit int) ([]RecallResult, error) {
	q := strings.ReplaceAll(query, `"`, `""`)
	rows, err := conn.Query(`
		SELECT w.id, w.content, w.importance, rank
		FROM fts_working f
		JOIN working_memory w ON w.rowid = f.rowid
		WHERE fts_working MATCH ?
		ORDER BY rank LIMIT ?`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecallResult
	for rows.Next() {
		var r RecallResult
		var rank float64
		if err := rows.Scan(&r.ID, &r.Content, &r.Importance, &rank); err != nil {
			continue
		}
		r.Score = 1.0 / (1.0 + math.Abs(rank))
		r.Table = "working_memory"
		out = append(out, r)
	}
	rows2, err := conn.Query(`
		SELECT e.id, e.content, e.importance, rank
		FROM fts_episodic f
		JOIN episodic_memory e ON e.rowid = f.rowid
		WHERE fts_episodic MATCH ?
		ORDER BY rank LIMIT ?`, q, limit)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var r RecallResult
			var rank float64
			if err := rows2.Scan(&r.ID, &r.Content, &r.Importance, &rank); err != nil {
				continue
			}
			r.Score = 1.0 / (1.0 + math.Abs(rank))
			r.Table = "episodic_memory"
			out = append(out, r)
		}
	}
	return out, nil
}

func recallPreferences(conn *sql.DB, query string, limit int) ([]RecallResult, error) {
	return recallPreferencesAt(conn, query, limit, true, nil)
}

func recallPreferencesAt(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time) ([]RecallResult, error) {
	return recallPreferencesWithDecay(conn, query, limit, includeConflicts, asOf, 0, 0)
}

func recallPreferencesWithDecay(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time, cFloor, halfLifeHours float64) ([]RecallResult, error) {
	return recallPreferencesFull(conn, query, limit, includeConflicts, asOf, RecallOptions{CFloor: cFloor, HalfLifeHours: halfLifeHours})
}

func recallPreferencesFull(conn *sql.DB, query string, limit int, includeConflicts bool, asOf *time.Time, opts RecallOptions) ([]RecallResult, error) {
	q := "%" + query + "%"
	filter := ""
	if !includeConflicts {
		filter = ` AND id NOT IN (SELECT c.conflicting_id FROM conflicts c JOIN preferences loser ON loser.id=c.conflicting_id JOIN preferences winner ON winner.id=c.preference_id WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence)
		          AND id NOT IN (SELECT c.preference_id FROM conflicts c JOIN preferences loser ON loser.id=c.preference_id JOIN preferences winner ON winner.id=c.conflicting_id WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence)`
	}
	rows, err := conn.Query(`
		SELECT id, statement, confidence, importance, last_reinforced_at FROM preferences
		WHERE valid_until IS NULL AND (statement LIKE ? OR category LIKE ?)`+filter+`
		ORDER BY confidence * importance DESC LIMIT ?`, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecallResult
	for rows.Next() {
		var id, stmt string
		var conf, imp float64
		var lastReinforced string
		if err := rows.Scan(&id, &stmt, &conf, &imp, &lastReinforced); err != nil {
			continue
		}
		eff := conf
		if asOf != nil && opts.HalfLifeHours > 0 {
			eff = decayAt(conf, opts.CFloor, opts.HalfLifeHours, lastReinforced, *asOf)
		}
		fresh := 1.0
		if opts.Freshness.Enabled {
			fresh = freshnessWeight(lastReinforced, asOf, opts.Freshness)
		}
		out = append(out, RecallResult{ID: id, Content: stmt, Score: eff * imp * fresh, Table: "preferences", Importance: imp})
	}
	return out, nil
}

// freshnessWeight applies the tiered T1/T2/T3 recency multiplier from config.
// Disabled by default (weight 1.0); enable via freshness.enabled.
func freshnessWeight(lastReinforcedAt string, asOf *time.Time, fw FreshnessWeights) float64 {
	t, err := time.Parse(time.RFC3339, lastReinforcedAt)
	if err != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", lastReinforcedAt)
	}
	now := time.Now().UTC()
	if asOf != nil {
		now = *asOf
	}
	ageDays := now.Sub(t).Hours() / 24
	if ageDays < 0 {
		return fw.T1Weight
	}
	if fw.T1Days > 0 && ageDays <= float64(fw.T1Days) {
		return fw.T1Weight
	}
	if fw.T2Days > 0 && ageDays <= float64(fw.T2Days) {
		return fw.T2Weight
	}
	return fw.T3Weight
}

// decayAt replays exponential decay pinned to the asOf instant instead of
// wall-clock, so `recall --as-of` is deterministic for a fixed DB state.
func decayAt(c, cFloor, halfLifeHours float64, lastReinforcedAt string, asOf time.Time) float64 {
	t, err := time.Parse(time.RFC3339, lastReinforcedAt)
	if err != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", lastReinforcedAt)
	}
	d := asOf.Sub(t).Hours()
	if d < 0 {
		return c
	}
	lambda := math.Log(2) / halfLifeHours
	decayed := c * math.Exp(-lambda*d)
	if decayed < cFloor {
		return cFloor
	}
	return decayed
}

func rrfMerge(a, b []RecallResult, k int) []RecallResult {
	scores := map[string]float64{}
	items := map[string]RecallResult{}
	for i, r := range a {
		scores[r.ID] += 1.0 / float64(k+i+1)
		items[r.ID] = r
	}
	for i, r := range b {
		scores[r.ID] += 1.0 / float64(k+i+1)
		if _, ok := items[r.ID]; !ok {
			items[r.ID] = r
		}
	}
	type scored struct {
		RecallResult
		Fused float64
	}
	var list []scored
	for id, s := range scores {
		r := items[id]
		r.Score = s
		list = append(list, scored{RecallResult: r, Fused: s})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].Fused > list[i].Fused {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	out := make([]RecallResult, len(list))
	for i, s := range list {
		out[i] = s.RecallResult
	}
	return out
}

func Sleep(conn *sql.DB) (int, error) {
	tx, err := conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, content, importance, session_id, created_at FROM working_memory WHERE consolidated_at IS NULL ORDER BY created_at ASC LIMIT 50`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id, content, sessionID, createdAt string
		importance float64
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content, &r.importance, &r.sessionID, &r.createdAt); err != nil {
			continue
		}
		batch = append(batch, r)
	}
	rows.Close()
	if len(batch) == 0 {
		_ = tx.Commit()
		return 0, nil
	}
	claimed := 0
	for _, r := range batch {
		eid := newID()
		now := time.Now().UTC().Format(time.RFC3339)
		meta, _ := json.Marshal(map[string]string{"source_id": r.id, "source_table": "working_memory"})
		_, err := tx.Exec(`INSERT INTO episodic_memory(id, content, importance, created_at, session_id, source) VALUES(?,?,?,?,?,?)`,
			eid, r.content, r.importance, now, r.sessionID, string(meta))
		if err != nil {
			continue
		}
		res, err := tx.Exec(`UPDATE working_memory SET consolidated_at=? WHERE id=? AND consolidated_at IS NULL`, now, r.id)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		_, _ = tx.Exec(`INSERT INTO consolidation_log(from_table, from_id, to_table, to_id) VALUES(?,?,?,?)`, "working_memory", r.id, "episodic_memory", eid)
		claimed++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return claimed, nil
}

// VectorSearchFallback is the cosine-similarity path used when the sqlite-vec
// build provides real embeddings. Without writers in memory_embeddings it
// scans zero rows; kept as the documented seam for the vec build.
func VectorSearchFallback(conn *sql.DB, queryVec []float64, limit int) ([]RecallResult, error) {
	rows, err := conn.Query(`SELECT memory_id, vector_json FROM memory_embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cand struct {
		id    string
		score float64
	}
	var cands []cand
	for rows.Next() {
		var mid, j string
		if err := rows.Scan(&mid, &j); err != nil {
			continue
		}
		var v []float64
		if err := json.Unmarshal([]byte(j), &v); err != nil {
			continue
		}
		s := cosine(queryVec, v)
		cands = append(cands, cand{mid, s})
	}
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].score > cands[i].score {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	if len(cands) > limit {
		cands = cands[:limit]
	}
	var out []RecallResult
	for _, c := range cands {
		var content string
		_ = conn.QueryRow(`SELECT content FROM working_memory WHERE id=? UNION ALL SELECT content FROM episodic_memory WHERE id=? LIMIT 1`, c.id, c.id).Scan(&content)
		if content == "" {
			content = c.id
		}
		out = append(out, RecallResult{ID: c.id, Content: content, Score: c.score, Table: "memory_embeddings"})
	}
	return out, nil
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
