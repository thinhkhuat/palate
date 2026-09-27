package judge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written to it — used to prove a refused rewrite logs its reason
// (phase-01 req 11), not just silently keeps the old text.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

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
	// Anchor the home to a temp dir: the gateway now stamps
	// state/last_gateway.json after every successful call, and a
	// CWD-resolved default home would litter the repo (and, run from the
	// operator's shell, the real palate home) with test stamps.
	cfg.Home = t.TempDir()
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
	if For(cfg).Name() != "llm" || Mode(cfg) != "llm (m via PALATE_LLM_MODEL)" {
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
//
// The seed is DISTILLED, not the STATED tier an explicit_prefer signal would
// give: learn.RewriteAllowed refuses to rewrite STATED text unconditionally
// (phase-01 req 8), so the "refines" step below needs a non-STATED target to
// stay eligible for a rewrite. A rewrite only ever trims, never grows (req 8
// bullet 3), so the refined text here is shorter than the seed's original —
// TestLLMJudgeRefinesRefusesStatedTarget below covers the STATED case.
func TestLLMJudgeRelations(t *testing.T) {
	db := testDB(t)
	seed, err := learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalLLMDistilled, Statement: "Prefers table-driven tests with subtests named after the case.", Category: "Testing", Weight: 0.5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv, calls := fakeGateway(t,
		verdictJSON("Prefers golden-file tests for renderers.", "Testing", 0.8, "new", ""),
		verdictJSON("Prefers table-driven tests.", "Testing", 0.9, "same", seed.ID),
		verdictJSON("Prefers table-driven tests.", "Testing", 0.85, "refines", seed.ID),
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
	if a.Relation != learn.RelationRefines || p.ID != seed.ID || p.Statement != "Prefers table-driven tests." {
		t.Fatalf("refines must rewrite a non-STATED target: %+v %+v", p, a)
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

// TestLLMJudgeRefinesRefusesStatedTarget proves phase-01 req 8/11 end to end:
// a STATED target (the common case — any explicit_prefer signal seeds
// STATED) is never rewritten by a "refines" verdict. The row still
// reinforces, the refused reason reaches stderr, and no "refine" event is
// written.
func TestLLMJudgeRefinesRefusesStatedTarget(t *testing.T) {
	db := testDB(t)
	seed, err := learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers table-driven tests.", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seed.TrustTier != "STATED" {
		t.Fatalf("an explicit_prefer seed must be STATED, got %s", seed.TrustTier)
	}
	srv, _ := fakeGateway(t, verdictJSON("Prefers table-driven tests with subtests named after the case.", "Testing", 0.85, "refines", seed.ID))
	cfg := llmCfg(t, srv)

	before := seed.Confidence
	var p *learn.Preference
	var a learn.Assessment
	stderr := captureStderr(t, func() {
		p, a, err = Apply(context.Background(), db, cfg, Input{Statement: "name subtests after the case", Signal: learn.SignalExplicitPrefer}, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Relation != learn.RelationRefines || p.ID != seed.ID {
		t.Fatalf("expected a refines verdict against the seed: %+v %+v", p, a)
	}
	if p.Statement != seed.Statement {
		t.Fatalf("a STATED target's text must be kept, got %q", p.Statement)
	}
	if p.Confidence <= before {
		t.Fatalf("the row must still reinforce even when the rewrite is refused: before=%f after=%f", before, p.Confidence)
	}
	if !strings.Contains(stderr, "refused") {
		t.Fatalf("a refused rewrite must log its reason to stderr, got %q", stderr)
	}
	var refines int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='refine'`).Scan(&refines)
	if refines != 0 {
		t.Fatalf("a refused rewrite must not log a refine event, got %d", refines)
	}
}

// TestJudgeTargetsOnlyCompareSet proves phase-03 req 2's guard still holds
// once the whole ledger is replaced by the compare set: a row excluded from
// the compare set (DISTILLED, one session, no wording overlap with the
// candidate) is a real, existing id the model was never shown. A verdict
// that names it as a target anyway must degrade to "new", exactly as an
// outright hallucinated id does.
func TestJudgeTargetsOnlyCompareSet(t *testing.T) {
	db := testDB(t)
	hidden, err := learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalLLMDistilled, Statement: "Prefers dark mode in every editor.", Category: "Tooling", Weight: 0.5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := fakeGateway(t, verdictJSON("Prefers tabs over spaces.", "Formatting", 0.7, "same", hidden.ID))
	cfg := llmCfg(t, srv)

	_, a, err := Apply(context.Background(), db, cfg, Input{Statement: "use tabs not spaces", Signal: learn.SignalExplicitPrefer}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Relation != learn.RelationNew || a.TargetID != "" {
		t.Fatalf("a target outside the compare set must degrade to new, got %+v", a)
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

// TestDistilledJudgedSameReinforces: a distilled paraphrase judged "same" by
// the model reinforces the existing row at the distiller's own confidence —
// no second bullet, whatever category the distiller guessed.
func TestDistilledJudgedSameReinforces(t *testing.T) {
	db := testDB(t)
	seed, err := learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalLLMDistilled, Statement: "Prefers table-driven tests.", Category: "Testing", Weight: 0.5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := fakeGateway(t, verdictJSON("Prefers table-driven tests.", "Testing", 0.6, "same", seed.ID))
	cfg := llmCfg(t, srv)

	before := seed.Confidence
	if err := ApplyDistilled(context.Background(), db, cfg, distill.DistilledStatement{Statement: "Likes tests laid out as a table of cases.", Category: "Workflow", Confidence: 0.6, Evidence: "asked for a case table"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("a judged 'same' must not add a row, got %d", rows)
	}
	var c float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE id=?`, seed.ID).Scan(&c)
	want := learn.Reinforce(before, 0.6, config.Default().LearningRate, config.Default().CFloor)
	if c != want {
		t.Fatalf("reinforced at the distiller's weight: got %.4f want %.4f (before %.4f)", c, want, before)
	}
	var evidence int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='reinforce' AND payload LIKE '%case table%'`).Scan(&evidence)
	if evidence != 1 {
		t.Fatalf("the reinforce event must carry the distiller's evidence")
	}
}

// TestLLMJudgeWholeProfileAndConfidenceHint: the prompt lists every active
// row with its category, not just the hinted category, and carries the
// caller's evidence confidence.
func TestLLMJudgeWholeProfileAndConfidenceHint(t *testing.T) {
	db := testDB(t)
	_, _ = learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers table-driven tests.", Category: "Testing"}, nil)
	_, _ = learn.ApplySignal(db, config.Default(), learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers tag-triggered releases.", Category: "Workflow"}, nil)

	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		body = req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": verdictJSON("Prefers tests as tables.", "Testing", 0.6, "new", "")}}}})
	}))
	t.Cleanup(srv.Close)
	cfg := llmCfg(t, srv)

	_, _, err := Apply(context.Background(), db, cfg, Input{Statement: "tests as tables", Signal: learn.SignalLLMDistilled, Category: "Testing", Confidence: 0.55}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Testing] Prefers table-driven tests.", "[Workflow] Prefers tag-triggered releases.", `evidence_confidence="0.55"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("judge prompt lacks %q:\n%s", want, body)
		}
	}
}

// Without a gateway the heuristic judge seeds a distilled row at the
// distiller's own confidence (capped at 0.9), not at weight×0.5.
func TestHeuristicDistilledSeedsAtEvidenceConfidence(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	p, a, err := Apply(context.Background(), db, cfg, Input{Statement: "Prefers golden files.", Signal: learn.SignalLLMDistilled, Category: "Testing", Confidence: 0.65}, "")
	if err != nil || a.By != "heuristic" || p.Confidence != 0.65 {
		t.Fatalf("got %+v %+v %v", p, a, err)
	}
	p, _, _ = Apply(context.Background(), db, cfg, Input{Statement: "Prefers very sure things.", Signal: learn.SignalLLMDistilled, Category: "Testing", Confidence: 0.99}, "")
	if p.Confidence != 0.9 {
		t.Fatalf("cap at 0.9, got %.2f", p.Confidence)
	}
}
