package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/storage"
)

func testStore(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	for _, k := range []string{"PALATE_LLM_ENDPOINT", "PALATE_LLM_MODEL", "PALATE_LLM_API_KEY"} {
		t.Setenv(k, "")
	}
	storage.SetOnDirtyFlush(nil)
	storage.Close()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Home = home
	cfg.DBPath = filepath.Join(home, "palate.db")
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storage.Close)
	return db, cfg
}

func gatewayReplying(t *testing.T, actions []Action) *distill.Gateway {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"actions": actions})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(body)}}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")
	cfg := config.Default()
	// Temp home: the gateway stamps state/last_gateway.json after every
	// successful call — never let that land in the CWD or the real home.
	cfg.Home = t.TempDir()
	cfg.LLM.Endpoint, cfg.LLM.Model = srv.URL+"/v1/chat/completions", "fake"
	gw, ok := distill.NewGatewayFromEnv(cfg)
	if !ok {
		t.Fatal("gateway not built")
	}
	return gw
}

// seed inserts distilled, one-session rows (candidates, rewritable text) aged
// past the 24 h window. STATED rows would be profile members whose text is
// never rewritten, which is not what these tests exercise.
func seed(t *testing.T, db *sql.DB, cfg config.Config, stmts ...string) []string {
	t.Helper()
	old := time.Now().UTC().Add(-48 * time.Hour)
	var ids []string
	for i, st := range stmts {
		id := fmt.Sprintf("seed%02d", len(ids)+i)
		for {
			var n int
			_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE id=?`, id).Scan(&n)
			if n == 0 {
				break
			}
			id += "x"
		}
		insertRow(t, db, rowSpec{id: id, stmt: st, reinforced: old, created: time.Now().UTC()})
		ids = append(ids, id)
	}
	return ids
}

func liveCount(db *sql.DB) int {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&n)
	return n
}

// TestConsolidateAppliesEveryActionType: auto mode applies merge, rewrite,
// rescore and retire through the events funnel with a backup first.
func TestConsolidateAppliesEveryActionType(t *testing.T) {
	db, cfg := testStore(t)
	ids := seed(t, db, cfg, "use table-driven tests", "table driven tests are preferred", "name tests after the case", "avoid sleep in tests", "use golden files", "prefer subtests", "keep tests fast", "mock the clock", "assert with cmp", "one file per package", "no global state", "fail fast", "use t.Helper", "parallel where safe", "no sleep-based sync")
	gw := gatewayReplying(t, []Action{
		{Type: "merge", IDs: []string{ids[0], ids[1]}, Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.85, Reason: "same"},
		{Type: "rewrite", ID: ids[2], Statement: "Names tests per case.", Reason: "third person"},
		{Type: "rescore", ID: ids[3], Confidence: 0.9, Reason: "reinforced often"},
		{Type: "retire", ID: ids[4], Reason: "one-off"},
	})
	dirty := 0
	storage.SetOnDirtyFlush(func(*sql.DB) { dirty++ })
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != "auto" || rep.Applied != 4 || rep.Skipped != 0 {
		t.Fatalf("report: %+v", rep)
	}
	if liveCount(db) != 13 {
		t.Fatalf("merge closes one and retire closes one: live=%d", liveCount(db))
	}
	var stmt string
	var conf float64
	_ = db.QueryRow(`SELECT statement, confidence FROM preferences WHERE id=?`, ids[0]).Scan(&stmt, &conf)
	if stmt != "Prefers table-driven tests." || conf != 0.85 {
		t.Fatalf("merge survivor: %q %.2f", stmt, conf)
	}
	_ = db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, ids[2]).Scan(&stmt)
	if stmt != "Names tests per case." {
		t.Fatalf("rewrite: %q", stmt)
	}
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE id=?`, ids[3]).Scan(&conf)
	if conf != 0.9 {
		t.Fatalf("rescore: %.2f", conf)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type LIKE 'consolidate_%'`).Scan(&n)
	if n != 4 {
		t.Fatalf("expected 4 consolidate events, got %d", n)
	}
	storage.FlushIfDirty()
	if dirty != 1 {
		t.Fatalf("actions must mark the store dirty once per flush, got %d", dirty)
	}
	if bak := storage.NewestBackup(cfg.BackupDir()); bak == "" {
		t.Fatal("auto mode must back up before applying")
	}
	var wm int64
	_ = db.QueryRow(`SELECT last_event_id FROM consolidations ORDER BY id DESC LIMIT 1`).Scan(&wm)
	if wm == 0 {
		t.Fatal("watermark not recorded")
	}
}

// TestConsolidateReviewModeProposesOnly: review mode changes nothing and
// files one proposal per action, which ApplyProposal can later run.
func TestConsolidateReviewModeProposesOnly(t *testing.T) {
	db, cfg := testStore(t)
	cfg.Distill.Mode = "review"
	ids := seed(t, db, cfg, "use table-driven tests", "keep tests fast", "use golden files")
	gw := gatewayReplying(t, []Action{{Type: "rewrite", ID: ids[0], Statement: "Prefers table tests.", Reason: "r"}})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil || rep.Mode != "review" || rep.Proposed != 1 || rep.Applied != 0 {
		t.Fatalf("report: %+v err=%v", rep, err)
	}
	var stmt, content string
	_ = db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, ids[0]).Scan(&stmt)
	if stmt != "use table-driven tests" {
		t.Fatal("review mode must not change rows")
	}
	_ = db.QueryRow(`SELECT content FROM proposals WHERE source='consolidate'`).Scan(&content)
	if err := ApplyProposal(db, cfg, content); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, ids[0]).Scan(&stmt)
	if stmt != "Prefers table tests." {
		t.Fatalf("promoting the proposal must apply it: %q", stmt)
	}
}

// TestConsolidateGuards: a shielded row (two sessions, reinforced now) is never
// retired, unknown ids are skipped, and a pass touching > 40 % is downgraded
// to proposals.
func TestConsolidateGuards(t *testing.T) {
	db, cfg := testStore(t)
	ids := seed(t, db, cfg, "a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	insertRow(t, db, rowSpec{id: "shield", stmt: "twice seen", sessions: []string{"s1", "s2"}})
	gw := gatewayReplying(t, []Action{
		{Type: "retire", ID: "shield", Reason: "fresh and repeated — must be blocked"},
		{Type: "retire", ID: "no-such-id", Reason: "unknown"},
		{Type: "rescore", ID: ids[1], Confidence: 0.6},
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil || rep.Skipped != 2 || rep.Applied != 1 || liveCount(db) != 11 {
		t.Fatalf("guards: %+v err=%v live=%d", rep, err, liveCount(db))
	}

	// > 40 % of rows touched → proposals even in auto mode
	db2, cfg2 := testStore(t)
	ids2 := seed(t, db2, cfg2, "a", "b", "c", "d", "e")
	var many []Action
	for _, id := range ids2[:3] {
		many = append(many, Action{Type: "rescore", ID: id, Confidence: 0.7})
	}
	gw2 := gatewayReplying(t, many)
	rep2, err := Run(context.Background(), db2, cfg2, gw2, RunOptions{})
	if err != nil || rep2.Mode != "review(guard)" || rep2.Applied != 0 || rep2.Proposed != 3 {
		t.Fatalf("40%% guard: %+v err=%v", rep2, err)
	}
}

// TestConsolidateDue: the watermark math behind SessionStart auto-fire.
func TestConsolidateDue(t *testing.T) {
	db, cfg := testStore(t)
	cfg.Consolidate.EveryEvents = 3
	seed(t, db, cfg, "a", "b") // 2 create events
	if since, _, due := Due(db, cfg); due || since != 2 {
		t.Fatalf("2 events, every 3: since=%d due=%v", since, due)
	}
	seed(t, db, cfg, "c")
	if _, _, due := Due(db, cfg); !due {
		t.Fatal("3 events, every 3: must be due")
	}
	gw := gatewayReplying(t, nil)
	if _, err := Run(context.Background(), db, cfg, gw, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if since, _, due := Due(db, cfg); due || since != 0 {
		t.Fatalf("after a pass the watermark resets: since=%d due=%v", since, due)
	}
	cfg.Consolidate.EveryEvents = 0
	seed(t, db, cfg, "d", "e", "f", "g")
	if _, _, due := Due(db, cfg); due {
		t.Fatal("every_events=0 disables auto-fire")
	}
}

// A rewrite carrying a category re-files the row; without one the category stays.
func TestRewriteCategory(t *testing.T) {
	db, cfg := testStore(t)
	ids := seed(t, db, cfg, "Prefers commits pushed without asking during an arc")
	if err := ApplyAction(db, cfg, Action{Type: "rewrite", ID: ids[0], Statement: "Wants commits pushed autonomously during an arc.", Category: "Workflow", Reason: "re-file"}); err != nil {
		t.Fatal(err)
	}
	var cat, stmt string
	_ = db.QueryRow(`SELECT category, statement FROM preferences WHERE id=?`, ids[0]).Scan(&cat, &stmt)
	if cat != "Workflow" || stmt != "Wants commits pushed autonomously during an arc." {
		t.Fatalf("rewrite with category: got %q / %q", cat, stmt)
	}
	// A second rewrite needs evidence newer than the first.
	if err := storage.AppendEvent(db, "reinforce", `{"id":"`+ids[0]+`"}`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAction(db, cfg, Action{Type: "rewrite", ID: ids[0], Statement: "Wants commits pushed autonomously.", Reason: "shorter"}); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT category FROM preferences WHERE id=?`, ids[0]).Scan(&cat)
	if cat != "Workflow" {
		t.Fatalf("rewrite without category must keep it, got %q", cat)
	}
}
