package render

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func TestRenderDeterministic(t *testing.T) {
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	cfg := config.Default()
	_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}, nil)
	a, _ := Render(db, RenderOptions{})
	b, _ := Render(db, RenderOptions{})
	if a != b {
		t.Fatalf("render not deterministic")
	}
}

func TestLint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASTE.md")
	_ = os.WriteFile(path, []byte("# Taste\n\n## Testing\n\n- use table-driven tests. Confidence: 0.40\n"), 0644)
	if diags := Lint(path); len(diags) != 0 {
		t.Fatalf("lint: %v", diags)
	}
	_ = os.WriteFile(path, []byte("no header\n- bad. Confidence: 9.9\n"), 0644)
	if diags := Lint(path); len(diags) == 0 {
		t.Fatalf("expected lint errors")
	}
}

func TestReimportQuarantine(t *testing.T) {
	db := openTestDB(t)
	var err error
	dir := t.TempDir()
	path := filepath.Join(dir, "TASTE.md")
	_ = os.WriteFile(path, []byte("# Taste\n\n## General\n\n- use early returns. Confidence: 0.90\n"), 0644)
	n, err := Reimport(db, config.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reimport n=%d", n)
	}
	var tier, src string
	var conf float64
	_ = db.QueryRow(`SELECT trust_tier, veracity, confidence FROM preferences LIMIT 1`).Scan(&tier, &src, &conf)
	if tier != "IMPORTED" || conf != 0.4 {
		t.Fatalf("quarantine: tier=%s conf=%v", tier, conf)
	}
	n2, _ := Reimport(db, config.Default(), path)
	if n2 != 0 {
		t.Fatalf("reimport should be idempotent, got %d", n2)
	}
}

// TestRenderHeaderAndProvenance: the header pins the last event id (no
// timestamp churn) and every bullet ends with its source and trust tier.
func TestRenderHeaderAndProvenance(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, source, trust_tier) VALUES('p1','General','Use tabs',0.8,'explicit_prefer','STATED')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events(event_type, payload) VALUES('create','{}')`); err != nil {
		t.Fatal(err)
	}
	out, err := Render(db, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "<!-- auto-maintained by palate after event #1;") {
		t.Fatalf("header missing or wrong: %q", out)
	}
	if !strings.Contains(out, "- Use tabs. Confidence: 0.80\n") {
		t.Fatalf("command-code bullet shape missing: %q", out)
	}
	if strings.Contains(out, "(explicit_prefer, STATED)") {
		t.Fatalf("internal metadata suffix must not leak into the render: %q", out)
	}
	if diags := lintString(t, out); len(diags) != 0 {
		t.Fatalf("Lint must accept the new format: %v", diags)
	}
}

// TestSyncUnchangedNoRewrite: an unchanged DB renders byte-identical and
// Sync reports no write the second time.
func TestSyncUnchangedNoRewrite(t *testing.T) {
	db := openTestDB(t)
	_, _ = db.Exec(`INSERT INTO preferences(id, category, statement, confidence, source, trust_tier) VALUES('p1','General','Use tabs',0.8,'explicit_prefer','STATED')`)
	path := filepath.Join(t.TempDir(), "nested", "TASTE.md")
	wrote, err := Sync(db, path, RenderOptions{})
	if err != nil || !wrote {
		t.Fatalf("first Sync must write: wrote=%v err=%v", wrote, err)
	}
	wrote, err = Sync(db, path, RenderOptions{})
	if err != nil || wrote {
		t.Fatalf("second Sync on an unchanged DB must not write: wrote=%v err=%v", wrote, err)
	}
	_, _ = db.Exec(`INSERT INTO events(event_type, payload) VALUES('create','{}')`)
	wrote, _ = Sync(db, path, RenderOptions{})
	if !wrote {
		t.Fatal("a new event changes the header and must be written")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	for _, k := range []string{"PALATE_LLM_ENDPOINT", "PALATE_LLM_MODEL", "PALATE_LLM_API_KEY"} {
		t.Setenv(k, "")
	}
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	return db
}

func lintString(t *testing.T, content string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "TASTE.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Lint(path)
}

// TestReimportMarksDirty: pull goes through the event funnel, so the
// auto-maintained file catches up on the import itself.
func TestReimportMarksDirty(t *testing.T) {
	db := openTestDB(t)
	flushes := 0
	storage.SetOnDirtyFlush(func(*sql.DB) { flushes++ })
	t.Cleanup(func() { storage.SetOnDirtyFlush(nil) })
	path := filepath.Join(t.TempDir(), "TASTE.md")
	if err := os.WriteFile(path, []byte("# Taste\n\n## Testing\n\n- use table-driven tests. Confidence: 0.40\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Reimport(db, config.Default(), path)
	if err != nil || n != 1 {
		t.Fatalf("Reimport: n=%d err=%v", n, err)
	}
	storage.FlushIfDirty()
	if flushes != 1 {
		t.Fatalf("Reimport must mark the store dirty (flushes=%d)", flushes)
	}
	var events int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='create' AND payload LIKE '%"signal":"imported"%'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected one create event for the imported row, got %d", events)
	}
}
