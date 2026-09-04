package learn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

type Preference struct {
	ID                 string  `json:"id"`
	Category           string  `json:"category"`
	Statement          string  `json:"statement"`
	Confidence         float64 `json:"confidence"`
	Importance         float64 `json:"importance"`
	Source             string  `json:"source"`
	Provenance         string  `json:"provenance"`
	ValidFrom          string  `json:"valid_from"`
	ValidUntil         *string `json:"valid_until"`
	CreatedAt          string  `json:"created_at"`
	LastReinforcedAt   string  `json:"last_reinforced_at"`
	ReinforcementCount int     `json:"reinforcement_count"`
	Owner              string  `json:"owner"`
	TrustTier          string  `json:"trust_tier"`
	Veracity           string  `json:"veracity"`
}

func Decay(c, cFloor, halfLifeHours float64, deltaHours float64) float64 {
	if halfLifeHours <= 0 {
		return c
	}
	lambda := math.Log(2) / halfLifeHours
	decayed := c * math.Exp(-lambda*deltaHours)
	if decayed < cFloor {
		return cFloor
	}
	return decayed
}

func Reinforce(c, w, learningRate, cFloor float64) float64 {
	v := c + (1-c)*w*learningRate
	if v < cFloor {
		v = cFloor
	}
	if v > 1.0 {
		v = 1.0
	}
	return v
}

func Weaken(c, w, learningRate, cFloor float64) float64 {
	v := c * (1 - w*learningRate)
	if v < cFloor {
		v = cFloor
	}
	if v > 1.0 {
		v = 1.0
	}
	if v < cFloor {
		return cFloor
	}
	return v
}

func InitialConfidence(w, cFloor float64) float64 {
	v := w * 0.5
	if v < cFloor {
		return cFloor
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}

func deltaHours(lastReinforcedAt string, asOf *time.Time) float64 {
	t, err := time.Parse(time.RFC3339, lastReinforcedAt)
	if err != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", lastReinforcedAt)
	}
	now := time.Now().UTC()
	if asOf != nil {
		now = *asOf
	}
	d := now.Sub(t).Hours()
	if d < 0 {
		return 0
	}
	return d
}

func ApplySignal(conn *sql.DB, cfg config.Config, sig Signal, asOf *time.Time) (*Preference, error) {
	w := cfg.WeightFor(string(sig.Type))
	if sig.Type == SignalUserDiff {
		w = cfg.Weights.UserDiff
	}
	existing, _ := findByStatement(conn, sig.Category, sig.Statement)
	now := time.Now().UTC()
	if asOf != nil {
		now = *asOf
	}
	nowStr := now.Format(time.RFC3339)

	if existing == nil && sig.Category != "" {
		existing, _ = findContradiction(conn, sig.Category, sig.Statement)
	}

	if existing != nil {
		dh := deltaHours(existing.LastReinforcedAt, asOf)
		c := Decay(existing.Confidence, cfg.CFloor, cfg.HalfLifeHours, dh)
		if isContradiction(existing.Statement, sig.Statement) {
			c = Weaken(c, w, cfg.LearningRate, cfg.CFloor)
			if c <= cfg.CFloor+1e-9 {
				_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1, valid_until=? WHERE id=?`, c, nowStr, nowStr, existing.ID)
				_ = storage.AppendEvent(conn, "weaken_close", fmt.Sprintf(`{"id":%q,"c":%f}`, existing.ID, c))
			} else {
				_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1 WHERE id=?`, c, nowStr, existing.ID)
				_ = storage.AppendEvent(conn, "weaken", fmt.Sprintf(`{"id":%q,"c":%f}`, existing.ID, c))
			}
			_ = insertConflict(conn, existing.ID, sig.Statement, w, cfg, nowStr)
			return existing, nil
		}
		c = Reinforce(c, w, cfg.LearningRate, cfg.CFloor)
		_, err := conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1, provenance=?
			WHERE id=?`, c, nowStr, appendProvenance(existing.Provenance, string(sig.Type)), existing.ID)
		if err != nil {
			return nil, err
		}
		_ = storage.AppendEvent(conn, "reinforce", fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q}`, existing.ID, c, sig.Type))
		existing.Confidence = c
		return existing, nil
	}

	if sig.Category == "" {
		sig.Category = InferCategoryFromStatement(sig.Statement)
	}
	conf := InitialConfidence(w, cfg.CFloor)
	id := newID()
	prov, _ := json.Marshal([]string{string(sig.Type)})
	trustTier := "STATED"
	if sig.Type == SignalImported {
		trustTier = "IMPORTED"
	}
	_, err := conn.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, reinforcement_count, trust_tier, veracity)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sig.Category, sig.Statement, conf, 0.5, string(sig.Type), string(prov), nowStr, nowStr, nowStr, 0, trustTier, string(sig.Type))
	if err != nil {
		return nil, err
	}
	_ = storage.AppendEvent(conn, "create", fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q}`, id, conf, sig.Type))
	return &Preference{ID: id, Category: sig.Category, Statement: sig.Statement, Confidence: conf, TrustTier: trustTier}, nil
}

func findByStatement(conn *sql.DB, category, statement string) (*Preference, error) {
	var p Preference
	var validUntil sql.NullString
	err := conn.QueryRow(`SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE statement=? AND category=? AND valid_until IS NULL LIMIT 1`, statement, category).Scan(
		&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &validUntil, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity)
	if err != nil {
		return nil, err
	}
	if validUntil.Valid {
		p.ValidUntil = &validUntil.String
	}
	return &p, nil
}

func isContradiction(a, b string) bool {
	na, nb := normalizeStatement(a), normalizeStatement(b)
	if na == "" || nb == "" || na == nb {
		return false
	}
	pa, pb := stripNegation(na), stripNegation(nb)
	negA, negB := isNegated(na), isNegated(nb)
	if pa == pb && negA != negB {
		return true
	}
	sa, sb := significantWords(na), significantWords(nb)
	onlyA, onlyB := diffWords(sa, sb)
	if len(onlyA) != 1 || len(onlyB) != 1 {
		return false
	}
	return antonymPair(onlyA, onlyB)
}

var negationWords = map[string]bool{
	"never": true, "not": true, "no": true, "don't": true, "dont": true,
	"avoid": true, "stop": true, "instead": true,
}

func normalizeStatement(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, ".!?,;:")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func stripNegation(s string) string {
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, ".!?,;:")
		if !negationWords[w] {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

func isNegated(s string) bool {
	for _, w := range strings.Fields(s) {
		if negationWords[strings.Trim(w, ".!?,;:")] {
			return true
		}
	}
	return false
}

var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "to": true,
	"for": true, "in": true, "on": true, "of": true, "and": true, "or": true,
	"use": true, "using": true, "with": true, "i": true, "you": true,
	"always": true, "prefer": true, "prefers": true,
}

func significantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(stripNegation(s)) {
		w = strings.Trim(w, ".!?,;:")
		if w == "" || stopWords[w] {
			continue
		}
		out[w] = true
	}
	return out
}

func diffWords(a, b map[string]bool) (map[string]bool, map[string]bool) {
	onlyA, onlyB := map[string]bool{}, map[string]bool{}
	for w := range a {
		if !b[w] {
			onlyA[w] = true
		}
	}
	for w := range b {
		if !a[w] {
			onlyB[w] = true
		}
	}
	return onlyA, onlyB
}

var antonymPairs = [][2]string{
	{"named", "default"},
	{"tabs", "spaces"},
	{"early", "nested"},
	{"small", "large"},
	{"static", "dynamic"},
	{"sync", "async"},
}

func antonymPair(onlyA, onlyB map[string]bool) bool {
	for _, p := range antonymPairs {
		if (onlyA[p[0]] && onlyB[p[1]]) || (onlyA[p[1]] && onlyB[p[0]]) {
			return true
		}
	}
	return false
}

// findContradiction scans live preferences in the same category for a
// statement contradicting the incoming one. Exact matches are handled by
// findByStatement; this covers near-misses like "use X" vs "never use X".
func findContradiction(conn *sql.DB, category, statement string) (*Preference, error) {
	rows, err := conn.Query(`SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE category=? AND valid_until IS NULL`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Preference
		var validUntil sql.NullString
		if err := rows.Scan(&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &validUntil, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity); err != nil {
			continue
		}
		if isContradiction(p.Statement, statement) {
			if validUntil.Valid {
				p.ValidUntil = &validUntil.String
			}
			return &p, nil
		}
	}
	return nil, sql.ErrNoRows
}

func insertConflict(conn *sql.DB, existingID, newStatement string, w float64, cfg config.Config, nowStr string) error {
	conf := InitialConfidence(w, cfg.CFloor)
	cat := InferCategoryFromStatement(newStatement)
	prov, _ := json.Marshal([]string{"contradiction"})
	newPrefID := newID()
	_, err := conn.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, trust_tier, veracity)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, newPrefID, cat, newStatement, conf, 0.5, "stated_preference", string(prov), nowStr, nowStr, nowStr, "STATED", "stated")
	if err != nil {
		return err
	}
	cid := newID()
	_, err = conn.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES(?,?,?,?)`, cid, existingID, newPrefID, "contradiction")
	return err
}

func appendProvenance(existing, sig string) string {
	var arr []string
	_ = json.Unmarshal([]byte(existing), &arr)
	arr = append(arr, sig)
	b, _ := json.Marshal(arr)
	return string(b)
}

func ListPreferences(conn *sql.DB, includeConflicted bool) ([]Preference, error) {
	q := `SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE valid_until IS NULL`
	if !includeConflicted {
		q += ` AND id NOT IN (SELECT c.conflicting_id FROM conflicts c JOIN preferences loser ON loser.id=c.conflicting_id JOIN preferences winner ON winner.id=c.preference_id WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence)
		          AND id NOT IN (SELECT c.preference_id FROM conflicts c JOIN preferences loser ON loser.id=c.preference_id JOIN preferences winner ON winner.id=c.conflicting_id WHERE c.resolved_at IS NULL AND loser.confidence < winner.confidence)`
	}
	q += ` ORDER BY category ASC, confidence DESC`
	rows, err := conn.Query(q)
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
	return out, nil
}

func newID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Nanosecond())
}
