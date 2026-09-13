package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	for _, k := range []string{"PALATE_LLM_ENDPOINT", "PALATE_LLM_MODEL", "PALATE_LLM_API_KEY", "PALATE_JUDGE"} {
		t.Setenv(k, "")
	}
	storage.SetOnDirtyFlush(nil)
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storage.Close)
	return db
}

// fakeGateway serves canned replies in order; body 500 when reply == "500".
func fakeGateway(t *testing.T, replies ...string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&calls, 1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		if replies[i] == "500" {
			http.Error(w, "boom", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": replies[i]}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func llmCfg(t *testing.T, srv *httptest.Server) config.Config {
	t.Helper()
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")
	cfg := config.Default()
	cfg.LLM.Endpoint, cfg.LLM.Model = srv.URL+"/v1/chat/completions", "fake"
	return cfg
}

func verdictJSON(stmt, cat string, conf float64, rel, target string) string {
	b, _ := json.Marshal(map[string]interface{}{"statement": stmt, "category": cat, "confidence": conf, "relation": rel, "target_id": target, "reason": "test"})
	return string(b)
}

func TestForPicksImplementation(t *testing.T) {
	testDB(t)
	cfg := config.Default()
	if For(cfg).Name() != "heuristic" || !strings.Contains(Mode(cfg), "no gateway") {
		t.Fatalf("no gateway must give the heuristic judge: %s / %s", For(cfg).Name(), Mode(cfg))
	}
	cfg.LLM.Endpoint, cfg.LLM.Model = "http://127.0.0.1:9/v1", "m"
	if For(cfg).Name() != "llm" || Mode(cfg) != "llm (m)" {
		t.Fatalf("gateway configured must give the llm judge: %s / %s", For(cfg).Name(), Mode(cfg))
	}
	cfg.Judge.Enabled = false
	if For(cfg).Name() != "heuristic" || !strings.Contains(Mode(cfg), "PALATE_JUDGE=0") {
		t.Fatalf("judge.enabled=false must fall back: %s / %s", For(cfg).Name(), Mode(cfg))
	}
}

// TestHeuristicJudgeMatchesApplySignal: for the same inputs the heuristic
// verdicts reproduce what ApplySignal did in 0.2.0 (statement, category,
// confidence, tier), so no-gateway users see no change.
func TestHeuristicJudgeMatchesApplySignal(t *testing.T) {
	cfg := config.Default()
	script := []struct {
		sig  learn.SignalType
		stmt string
	}{
		{learn.SignalExplicitPrefer, "use table-driven tests"},
		{learn.SignalExplicitPrefer, "use table-driven tests"}, // exact duplicate → reinforce
		{learn.SignalExplicitReject, "never use panic in handlers"},
		{learn.SignalExplicitAccept, "handle errors with wrapped context"},
		{learn.SignalExplicitCorrection, "prefer early returns over nested ifs"},
		{learn.SignalImported, "import only what is used"},
		{learn.SignalExplicitPrefer, "never use table-driven tests"}, // contradiction of the first row
	}
	oldDB := testDB(t)
	for _, s := range script {
		if _, err := learn.ApplySignal(oldDB, cfg, learn.Signal{Type: s.sig, Statement: s.stmt, Category: learn.InferCategoryFromStatement(s.stmt)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	oldRows := shape(t, oldDB)

	newDB := testDB(t)
	for _, s := range script {
		if _, _, err := Apply(context.Background(), newDB, cfg, Input{Statement: s.stmt, Signal: s.sig}, ""); err != nil {
			t.Fatal(err)
		}
	}
	newRows := shape(t, newDB)
	// Two documented, deliberate differences from the old path:
	//  - imports: Reimport seeded 0.4 directly, ApplySignal 0.2 — the judge keeps Reimport's 0.4;
	//  - the contradicting row's source: insertConflict hardcoded "stated_preference";
	//    the judge records the real signal. Everything else must match byte for byte.
	for i := range oldRows {
		if strings.Contains(oldRows[i], "|imported|") {
			oldRows[i] = strings.Replace(oldRows[i], "|0.20|", "|0.40|", 1)
		}
		if strings.HasPrefix(oldRows[i], "Testing|never use table-driven tests|") {
			oldRows[i] = strings.Replace(oldRows[i], "|stated_preference|", "|explicit_prefer|", 1)
		}
	}
	if strings.Join(oldRows, "\n") != strings.Join(newRows, "\n") {
		t.Fatalf("heuristic judge drifted from ApplySignal:\nold:\n%s\nnew:\n%s", strings.Join(oldRows, "\n"), strings.Join(newRows, "\n"))
	}
}

func shape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT category, statement, printf('%.2f', confidence), source, trust_tier, reinforcement_count FROM preferences WHERE valid_until IS NULL ORDER BY category, statement`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c, s, conf, src, tier string
		var n int
		_ = rows.Scan(&c, &s, &conf, &src, &tier, &n)
		out = append(out, strings.Join([]string{c, s, conf, src, tier, string(rune('0' + n))}, "|"))
	}
	return out
}

// TestLLMJudgeRelations drives Apply through a fake gateway for each verdict.
func TestLLMJudgeRelations(t *testing.T) {
	db := testDB(t)
	// seed one row the model can point at
	seed, err := learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers table-driven tests.", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv, calls := fakeGateway(t,
		verdictJSON("Prefers golden-file tests for renderers.", "Testing", 0.8, "new", ""),
		verdictJSON("Prefers table-driven tests.", "Testing", 0.9, "same", seed.ID),
		verdictJSON("Prefers table-driven tests with subtests named after the case.", "Testing", 0.85, "refines", seed.ID),
		verdictJSON("Prefers one assertion per test function.", "Testing", 0.7, "contradicts", seed.ID),
		verdictJSON("Prefers something.", "Testing", 0.7, "same", "not-a-real-id"),
	)
	cfg := llmCfg(t, srv)

	p, a, err := Apply(context.Background(), db, cfg, Input{Statement: "i like golden files for the renderer tests", Signal: learn.SignalExplicitPrefer}, "")
	if err != nil || a.By != "llm" || a.Relation != learn.RelationNew || p.Statement != "Prefers golden-file tests for renderers." || p.Confidence != 0.8 || p.Category != "Testing" {
		t.Fatalf("new: %+v %+v %v", p, a, err)
	}

	before := seed.Confidence
	p, a, _ = Apply(context.Background(), db, cfg, Input{Statement: "table driven tests please", Signal: learn.SignalExplicitPrefer}, "")
	if a.Relation != learn.RelationSame || p.ID != seed.ID || p.Confidence <= before {
		t.Fatalf("same must reinforce the target: %+v %+v", p, a)
	}

	p, a, _ = Apply(context.Background(), db, cfg, Input{Statement: "name subtests after the case", Signal: learn.SignalExplicitPrefer}, "")
	if a.Relation != learn.RelationRefines || p.ID != seed.ID || p.Statement != "Prefers table-driven tests with subtests named after the case." {
		t.Fatalf("refines must rewrite the target: %+v %+v", p, a)
	}
	var refines int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='refine'`).Scan(&refines)
	if refines != 1 {
		t.Fatalf("refine event missing: %d", refines)
	}

	var before2 int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&before2)
	p, a, _ = Apply(context.Background(), db, cfg, Input{Statement: "one assertion per test", Signal: learn.SignalExplicitPrefer}, "")
	if a.Relation != learn.RelationContradicts || p.ID == seed.ID || p.Statement != "Prefers one assertion per test function." || p.Confidence != 0.7 {
		t.Fatalf("contradicts must insert the judged new side: %+v %+v", p, a)
	}
	var conflicts, after2 int
	_ = db.QueryRow(`SELECT count(*) FROM conflicts WHERE resolved_at IS NULL`).Scan(&conflicts)
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&after2)
	if conflicts != 1 || after2 != before2+1 {
		t.Fatalf("contradicts must open one conflict and add exactly one row: conflicts=%d rows %d→%d", conflicts, before2, after2)
	}

	_, a, _ = Apply(context.Background(), db, cfg, Input{Statement: "something", Signal: learn.SignalExplicitPrefer}, "")
	if a.Relation != learn.RelationNew {
		t.Fatalf("an unknown target_id must degrade to new, got %s", a.Relation)
	}
	if *calls != 5 {
		t.Fatalf("expected 5 gateway calls, got %d", *calls)
	}
	var hits int
	_ = db.QueryRow(`SELECT hits FROM capture_counters WHERE signal_type='judge_llm'`).Scan(&hits)
	if hits != 5 {
		t.Fatalf("judge_llm counter = %d, want 5", hits)
	}
}

// TestLLMJudgeFallback: garbage twice (retry exhausted) and a 500 both land
// the write via the heuristic with the fallback counter bumped.
func TestLLMJudgeFallback(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, "not json at all", "still not json", "500")
	cfg := llmCfg(t, srv)
	p, a, err := Apply(context.Background(), db, cfg, Input{Statement: "use table-driven tests", Signal: learn.SignalExplicitPrefer}, "")
	if err != nil || a.By != "heuristic" || p.Statement != "use table-driven tests" || p.Confidence != 0.4 {
		t.Fatalf("garbage reply must fall back to the heuristic verdict: %+v %+v %v", p, a, err)
	}
	p, a, err = Apply(context.Background(), db, cfg, Input{Statement: "never use panic", Signal: learn.SignalExplicitReject}, "")
	if err != nil || a.By != "heuristic" {
		t.Fatalf("500 must fall back: %+v %+v %v", p, a, err)
	}
	var drops int
	_ = db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='judge_fallback'`).Scan(&drops)
	if drops != 2 {
		t.Fatalf("judge_fallback drops = %d, want 2", drops)
	}
}

// TestLLMJudgeStripsFenceAndClamps: a fenced reply parses; confidence 1.0 clamps to 0.95.
func TestLLMJudgeStripsFenceAndClamps(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, "```json\n"+verdictJSON("Prefers tabs.", "Formatting", 1.0, "new", "")+"\n```")
	cfg := llmCfg(t, srv)
	p, a, err := Apply(context.Background(), db, cfg, Input{Statement: "tabs", Signal: learn.SignalExplicitPrefer}, "")
	if err != nil || a.By != "llm" || p.Confidence != 0.95 || p.Category != "Formatting" {
		t.Fatalf("fenced/clamped: %+v %+v %v", p, a, err)
	}
}

// keep the distill import honest (NewGatewayFromEnv is what For uses)
var _ = distill.NewGatewayFromEnv
