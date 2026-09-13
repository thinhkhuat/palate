package learn

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/safety"
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
	} else if sig.Type == SignalLLMDistilled {
		w = sig.Weight
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
	if existing == nil && sig.Category != "" && sig.Type == SignalLLMDistilled {
		existing, _ = findNearDuplicate(conn, sig.Category, sig.Statement)
	}

	if existing != nil {
		dh := deltaHours(existing.LastReinforcedAt, asOf)
		c := Decay(existing.Confidence, cfg.CFloor, cfg.HalfLifeHours, dh)
		if isContradiction(existing.Statement, sig.Statement) {
			c = Weaken(c, w, cfg.LearningRate, cfg.CFloor)
			if c <= cfg.CFloor+1e-9 {
				_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1, valid_until=? WHERE id=?`, c, nowStr, nowStr, existing.ID)
				_ = storage.AppendEvent(conn, "weaken_close", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f}`, existing.ID, c), sig.Evidence))
			} else {
				_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1 WHERE id=?`, c, nowStr, existing.ID)
				_ = storage.AppendEvent(conn, "weaken", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f}`, existing.ID, c), sig.Evidence))
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
		_ = storage.AppendEvent(conn, "reinforce", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q}`, existing.ID, c, sig.Type), sig.Evidence))
		existing.Confidence = c
		return existing, nil
	}

	if sig.Category == "" {
		sig.Category = InferCategoryFromStatement(sig.Statement)
	}
	conf := InitialConfidence(w, cfg.CFloor)
	if sig.Type == SignalLLMDistilled {
		// A distilled statement seeds at the model's own confidence (capped so a
		// single pass can never look certain); reinforcement still governs growth.
		conf = distilledSeed(w, cfg.CFloor)
	}
	id := newID()
	prov, _ := json.Marshal([]string{string(sig.Type)})
	trustTier := "STATED"
	if sig.Type == SignalImported {
		trustTier = "IMPORTED"
	} else if sig.Type == SignalLLMDistilled {
		trustTier = "DISTILLED"
	}
	_, err := conn.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, reinforcement_count, trust_tier, veracity)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sig.Category, sig.Statement, conf, 0.5, string(sig.Type), string(prov), nowStr, nowStr, nowStr, 0, trustTier, string(sig.Type))
	if err != nil {
		return nil, err
	}
	_ = storage.AppendEvent(conn, "create", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q}`, id, conf, sig.Type), sig.Evidence))
	return &Preference{ID: id, Category: sig.Category, Statement: sig.Statement, Confidence: conf, TrustTier: trustTier}, nil
}

// withEvidence inserts a censored "evidence" field into a JSON object
// payload built with fmt.Sprintf, just before its closing brace. Evidence
// is censored a second time here (already censored once before it reached
// the gateway) since this call site is the last stop before it lands in
// the events table.
func withEvidence(payload, evidence string) string {
	if evidence == "" {
		return payload
	}
	b, _ := json.Marshal(safety.Censor(evidence))
	return strings.TrimSuffix(payload, "}") + `,"evidence":` + string(b) + "}"
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

// nearDuplicateThreshold is the token-set Jaccard similarity, over
// hyphen-split/lowercase/punctuation-stripped/stop-word-stripped tokens, at
// or above which a restated llm_distilled statement reinforces an existing
// preference instead of inserting a near-twin. Tuned against the plan's own
// canonical pair ("Prefers table-driven tests." vs "Prefer table driven
// tests in Go") which measures 0.75 under this tokenizer — 0.8 does not
// clear it.
const nearDuplicateThreshold = 0.7

// findNearDuplicate scans live preferences in the same category for one
// whose token set is Jaccard-similar enough to statement to be the same
// preference restated differently. It reuses normalizeStatement and
// significantWords but hyphen-splits first — only here, never in the
// shared helpers isContradiction also depends on — so "table-driven" and
// "table driven" tokenize the same way.
func findNearDuplicate(conn *sql.DB, category, statement string) (*Preference, error) {
	target := nearDupWords(statement)
	if len(target) == 0 {
		return nil, sql.ErrNoRows
	}
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
		if jaccard(target, nearDupWords(p.Statement)) >= nearDuplicateThreshold {
			if validUntil.Valid {
				p.ValidUntil = &validUntil.String
			}
			return &p, nil
		}
	}
	return nil, sql.ErrNoRows
}

func nearDupWords(s string) map[string]bool {
	return significantWords(strings.ReplaceAll(normalizeStatement(s), "-", " "))
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
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

// newID keeps a sortable UnixNano prefix and adds 8 random bytes so that
// back-to-back calls within the same clock tick never collide.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%x", time.Now().UnixNano(), b)
}

// distilledSeedCap bounds the confidence a distilled statement can start
// with, whatever the model claimed.
const distilledSeedCap = 0.9

func distilledSeed(w, cFloor float64) float64 {
	switch {
	case w < cFloor:
		return cFloor
	case w > distilledSeedCap:
		return distilledSeedCap
	}
	return w
}

// Relation is a judge's verdict on how a candidate statement relates to the
// existing rows of its category.
type Relation string

const (
	RelationNew         Relation = "new"
	RelationSame        Relation = "same"
	RelationRefines     Relation = "refines"
	RelationContradicts Relation = "contradicts"
)

// Assessment is what a judge (LLM or heuristic) decides for one write:
// the statement to store, its category and confidence, and its relation to
// an existing row (TargetID set for same/refines/contradicts). By carries
// "llm" or "heuristic" for provenance.
type Assessment struct {
	Statement  string
	Category   string
	Confidence float64
	Relation   Relation
	TargetID   string
	Reason     string
	By         string
}

// ActiveInCategory lists the live rows a judge compares a candidate against.
func ActiveInCategory(conn *sql.DB, category string) ([]Preference, error) {
	rows, err := conn.Query(`SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE category=? AND valid_until IS NULL ORDER BY confidence DESC, id ASC`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preference
	for rows.Next() {
		var p Preference
		var validUntil sql.NullString
		if err := rows.Scan(&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &validUntil, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Exported lookups so the heuristic judge can reproduce ApplySignal's
// matching exactly.
func FindByStatement(conn *sql.DB, category, statement string) (*Preference, error) {
	return findByStatement(conn, category, statement)
}
func FindContradiction(conn *sql.DB, category, statement string) (*Preference, error) {
	return findContradiction(conn, category, statement)
}
func FindNearDuplicate(conn *sql.DB, category, statement string) (*Preference, error) {
	return findNearDuplicate(conn, category, statement)
}

// ClampConfidence keeps a judged confidence inside [cFloor, 0.95]: a single
// verdict never looks certain; reinforcement governs growth from there.
func ClampConfidence(c, cFloor float64) float64 {
	if c < cFloor {
		return cFloor
	}
	if c > 0.95 {
		return 0.95
	}
	return c
}

// ApplyJudged stores a write according to a judge's Assessment. It reuses
// ApplySignal's events, tiers and columns so render, recall, decay and
// verify keep working unchanged:
//
//	new         → insert at the judged confidence
//	same        → reinforce the target
//	refines     → reinforce the target and replace its statement (event
//	              "refine" keeps the old text)
//	contradicts → weaken the target, open a conflict, insert the new row
func ApplyJudged(conn *sql.DB, cfg config.Config, sig Signal, a Assessment) (*Preference, error) {
	w := cfg.WeightFor(string(sig.Type))
	now := time.Now().UTC().Format(time.RFC3339)
	if a.Category == "" {
		a.Category = InferCategoryFromStatement(a.Statement)
	}
	var target *Preference
	if a.TargetID != "" && a.Relation != RelationNew {
		target = preferenceByID(conn, a.TargetID)
	}
	if target == nil {
		a.Relation = RelationNew
	}
	switch a.Relation {
	case RelationSame, RelationRefines:
		dh := deltaHours(target.LastReinforcedAt, nil)
		c := Reinforce(Decay(target.Confidence, cfg.CFloor, cfg.HalfLifeHours, dh), w, cfg.LearningRate, cfg.CFloor)
		stmt := target.Statement
		if a.Relation == RelationRefines && a.Statement != "" && a.Statement != target.Statement {
			stmt = a.Statement
			_ = storage.AppendEvent(conn, "refine", withEvidence(fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"by":%q}`, target.ID, target.Statement, stmt, a.By), sig.Evidence))
		}
		if _, err := conn.Exec(`UPDATE preferences SET statement=?, confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1, provenance=? WHERE id=?`,
			stmt, c, now, appendProvenance(target.Provenance, string(sig.Type)+"/judged:"+a.By), target.ID); err != nil {
			return nil, err
		}
		_ = storage.AppendEvent(conn, "reinforce", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q,"by":%q}`, target.ID, c, sig.Type, a.By), sig.Evidence))
		target.Statement, target.Confidence = stmt, c
		return target, nil
	case RelationContradicts:
		dh := deltaHours(target.LastReinforcedAt, nil)
		c := Weaken(Decay(target.Confidence, cfg.CFloor, cfg.HalfLifeHours, dh), w, cfg.LearningRate, cfg.CFloor)
		if c <= cfg.CFloor+1e-9 {
			_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1, valid_until=? WHERE id=?`, c, now, now, target.ID)
			_ = storage.AppendEvent(conn, "weaken_close", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"by":%q}`, target.ID, c, a.By), sig.Evidence))
		} else {
			_, _ = conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=?, reinforcement_count=reinforcement_count+1 WHERE id=?`, c, now, target.ID)
			_ = storage.AppendEvent(conn, "weaken", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"by":%q}`, target.ID, c, a.By), sig.Evidence))
		}
		_ = insertConflict(conn, target.ID, a.Statement, w, cfg, now)
	}
	// new (or contradicts: the new side is stored as its own row)
	conf := ClampConfidence(a.Confidence, cfg.CFloor)
	id := newID()
	prov, _ := json.Marshal([]string{string(sig.Type), "judged:" + a.By})
	trustTier := "STATED"
	if sig.Type == SignalImported {
		trustTier = "IMPORTED"
	} else if sig.Type == SignalLLMDistilled {
		trustTier = "DISTILLED"
	}
	if _, err := conn.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, reinforcement_count, trust_tier, veracity)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, a.Category, a.Statement, conf, 0.5, string(sig.Type), string(prov), now, now, now, 0, trustTier, string(sig.Type)); err != nil {
		return nil, err
	}
	_ = storage.AppendEvent(conn, "create", withEvidence(fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q,"by":%q}`, id, conf, sig.Type, a.By), sig.Evidence))
	return &Preference{ID: id, Category: a.Category, Statement: a.Statement, Confidence: conf, TrustTier: trustTier}, nil
}

func preferenceByID(conn *sql.DB, id string) *Preference {
	var p Preference
	var validUntil sql.NullString
	err := conn.QueryRow(`SELECT id, category, statement, confidence, importance, source, provenance, valid_from, valid_until, created_at, last_reinforced_at, reinforcement_count, owner, trust_tier, veracity
		FROM preferences WHERE id=? AND valid_until IS NULL LIMIT 1`, id).Scan(
		&p.ID, &p.Category, &p.Statement, &p.Confidence, &p.Importance, &p.Source, &p.Provenance, &p.ValidFrom, &validUntil, &p.CreatedAt, &p.LastReinforcedAt, &p.ReinforcementCount, &p.Owner, &p.TrustTier, &p.Veracity)
	if err != nil {
		return nil
	}
	return &p
}
