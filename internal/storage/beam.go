package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
	if limit <= 0 {
		limit = 10
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return recallRecent(conn, limit)
	}
	ftsResults, _ := ftsSearch(conn, query, limit*2)
	prefs, _ := recallPreferences(conn, query, limit*2)
	merged := rrfMerge(ftsResults, prefs, 60)
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
	q := "%" + query + "%"
	rows, err := conn.Query(`
		SELECT id, statement, confidence, importance FROM preferences
		WHERE valid_until IS NULL AND (statement LIKE ? OR category LIKE ?)
		ORDER BY confidence * importance DESC LIMIT ?`, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecallResult
	for rows.Next() {
		var id, stmt string
		var conf, imp float64
		if err := rows.Scan(&id, &stmt, &conf, &imp); err != nil {
			continue
		}
		out = append(out, RecallResult{ID: id, Content: stmt, Score: conf * imp, Table: "preferences", Importance: imp})
	}
	return out, nil
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

func HybridScore(vecScore, ftsScore, importance, recencyDecay float64, cfgVec, cfgFTS, cfgImp float64) float64 {
	if recencyDecay == 0 {
		recencyDecay = 1
	}
	return (cfgVec*vecScore + cfgFTS*ftsScore + cfgImp*importance) * recencyDecay
}

func RecencyDecay(ageHours, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 {
		return 1
	}
	lambda := math.Log(2) / halfLifeHours
	return math.Exp(-lambda * ageHours)
}
