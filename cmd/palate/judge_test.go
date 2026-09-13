package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func verdictServer(t *testing.T, statement, category string, conf float64) *httptest.Server {
	t.Helper()
	reply, _ := json.Marshal(map[string]interface{}{"statement": statement, "category": category, "confidence": conf, "relation": "new", "target_id": "", "reason": "test"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(reply)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPreferJudgedByGateway: the CLI stores what the model decided, not the
// raw words, and reports the verdict.
func TestPreferJudgedByGateway(t *testing.T) {
	setupTempProject(t)
	srv := verdictServer(t, "Prefers table-driven tests.", "Testing", 0.8)
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")
	out, err := runCLI(t, "prefer", "i like table driven tests")
	if err != nil || !strings.Contains(out, "prefer recorded (Testing, new, c=0.80, llm)") {
		t.Fatalf("prefer: err=%v out=%q", err, out)
	}
	storage.Close()
	if got := tasteOnDisk(t); !strings.Contains(got, "- Prefers table-driven tests. Confidence: 0.80 (explicit_prefer, STATED)") {
		t.Fatalf("TASTE.md must carry the judged row:\n%s", got)
	}
	storage.Close()
	out, _ = runCLI(t, "doctor")
	if !strings.Contains(out, "judge: llm (fake)") {
		t.Fatalf("doctor must name the llm judge:\n%s", out)
	}
	storage.Close()
	out, _ = runCLI(t, "stats")
	if !strings.Contains(out, "judge_llm") {
		t.Fatalf("stats must show the judge_llm counter:\n%s", out)
	}
}

// TestGoldenFallbackExport: with no gateway a fixed script of explicit writes
// produces exactly the rows 0.2.0 produced (category, statement, confidence,
// source, tier).
func TestGoldenFallbackExport(t *testing.T) {
	setupTempProject(t)
	for _, args := range [][]string{
		{"prefer", "use table-driven tests"},
		{"prefer", "use table-driven tests"},
		{"correct", "used panic", "swallows context", "return an error", "reviewing handlers"},
		{"prefer", "prefer early returns over nested ifs"},
	} {
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		storage.Close()
	}
	out, err := runCLI(t, "doctor")
	if err != nil || !strings.Contains(out, "judge: heuristic (no gateway)") {
		t.Fatalf("doctor: %v %s", err, out)
	}
	storage.Close()
	cfg := loadConfig()
	db := openDB(cfg)
	rows, _ := db.Query(`SELECT category, statement, printf('%.2f', confidence), source, trust_tier, reinforcement_count FROM preferences WHERE valid_until IS NULL ORDER BY category, statement`)
	var got []string
	for rows.Next() {
		var c, s, conf, src, tier string
		var n int
		_ = rows.Scan(&c, &s, &conf, &src, &tier, &n)
		got = append(got, strings.Join([]string{c, s, conf, src, tier}, "|")+"|"+string(rune('0'+n)))
	}
	rows.Close()
	// the oracle is 0.2.0's own path: learn.ApplySignal on a second store
	storage.Close()
	setupTempProject(t)
	cfg2 := loadConfig()
	db2 := openDB(cfg2)
	for _, sc := range []struct {
		sig  learn.SignalType
		stmt string
	}{
		{learn.SignalExplicitPrefer, "use table-driven tests"},
		{learn.SignalExplicitPrefer, "use table-driven tests"},
		{learn.SignalExplicitCorrection, "return an error"},
		{learn.SignalExplicitPrefer, "prefer early returns over nested ifs"},
	} {
		if _, err := learn.ApplySignal(db2, cfg2, learn.Signal{Type: sc.sig, Statement: sc.stmt, Category: learn.InferCategoryFromStatement(sc.stmt)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows2, _ := db2.Query(`SELECT category, statement, printf('%.2f', confidence), source, trust_tier, reinforcement_count FROM preferences WHERE valid_until IS NULL ORDER BY category, statement`)
	var want []string
	for rows2.Next() {
		var c, s, conf, src, tier string
		var n int
		_ = rows2.Scan(&c, &s, &conf, &src, &tier, &n)
		want = append(want, strings.Join([]string{c, s, conf, src, tier}, "|")+"|"+string(rune('0'+n)))
	}
	rows2.Close()
	if len(want) != 3 || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("golden fallback drifted:\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func actionsServer(t *testing.T, actions []map[string]interface{}) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"actions": actions})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(body)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// installConsolidateSpy swaps the auto-fire seam for a counter.
func installConsolidateSpy(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := spawnConsolidate
	spawnConsolidate = func(string) error { n++; return nil }
	t.Cleanup(func() { spawnConsolidate = orig })
	return &n
}

// TestConsolidateVerbAndPromote: `palate consolidate` in review mode files a
// proposal; `palate promote <id>` applies the stored action.
func TestConsolidateVerbAndPromote(t *testing.T) {
	setupTempProject(t)
	// no gateway → the verb must say so
	if _, err := runCLI(t, "consolidate"); err == nil || !strings.Contains(err.Error(), "no LLM gateway") {
		t.Fatalf("consolidate without a gateway must refuse: %v", err)
	}
	storage.Close()
	for _, s := range []string{"use table-driven tests", "keep tests fast", "use golden files"} {
		if _, err := runCLI(t, "prefer", s); err != nil {
			t.Fatal(err)
		}
		storage.Close()
	}
	cfg := loadConfig()
	db := openDB(cfg)
	var id string
	_ = db.QueryRow(`SELECT id FROM preferences WHERE statement='use table-driven tests'`).Scan(&id)
	storage.Close()
	srv := actionsServer(t, []map[string]interface{}{{"type": "rewrite", "id": id, "statement": "Prefers table-driven tests.", "reason": "third person"}})
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")
	if err := os.WriteFile("config.yaml", []byte("distill:\n  mode: review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "consolidate")
	if err != nil || !strings.Contains(out, "1 proposed") || !strings.Contains(out, "mode=review") {
		t.Fatalf("consolidate: err=%v out=%s", err, out)
	}
	storage.Close()
	db = openDB(loadConfig())
	var pid string
	_ = db.QueryRow(`SELECT id FROM proposals WHERE source='consolidate' AND status='pending'`).Scan(&pid)
	storage.Close()
	if pid == "" {
		t.Fatal("no consolidation proposal filed")
	}
	if _, err := runCLI(t, "promote", pid); err != nil {
		t.Fatalf("promote: %v", err)
	}
	storage.Close()
	if got := tasteOnDisk(t); !strings.Contains(got, "Prefers table-driven tests.") {
		t.Fatalf("promoted rewrite must reach TASTE.md:\n%s", got)
	}
	storage.Close()
	out, _ = runCLI(t, "doctor")
	if !strings.Contains(out, "consolidate: 0 events since last pass") && !strings.Contains(out, "consolidate: ") {
		t.Fatalf("doctor must print the consolidate line:\n%s", out)
	}
}

// TestAutoConsolidateOnSessionStart: SessionStart spawns the pass only past
// the watermark, and only with a gateway configured.
func TestAutoConsolidateOnSessionStart(t *testing.T) {
	setupTempProject(t)
	installSpawnSpy(t)
	n := installConsolidateSpy(t)
	t.Setenv("PALATE_CONSOLIDATE_EVERY", "2")

	runHook(t, `{"session_id":"s","transcript_path":"/tmp/t.jsonl"}`, "hook", "session-start")
	if *n != 0 {
		t.Fatal("no gateway → never auto-fire")
	}
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:9/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "x")
	runHook(t, `{"session_id":"s","transcript_path":"/tmp/t.jsonl"}`, "hook", "session-start")
	if *n != 0 {
		t.Fatal("0 events since the last pass → not due")
	}
	for _, s := range []string{"use table-driven tests", "keep tests fast"} {
		t.Setenv("PALATE_JUDGE", "0") // keep the writes heuristic; the fake endpoint is dead
		if _, err := runCLI(t, "prefer", s); err != nil {
			t.Fatal(err)
		}
		storage.Close()
	}
	t.Setenv("PALATE_JUDGE", "")
	start := time.Now()
	runHook(t, `{"session_id":"s","transcript_path":"/tmp/t.jsonl"}`, "hook", "session-start")
	if *n != 1 {
		t.Fatalf("2 events, every 2 → must auto-fire once, got %d", *n)
	}
	if time.Since(start) > time.Second {
		t.Fatal("auto-fire must not block the hook")
	}
}
