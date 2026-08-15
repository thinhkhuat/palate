package render

import (
	"os"
	"path/filepath"
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
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	dir := t.TempDir()
	path := filepath.Join(dir, "TASTE.md")
	_ = os.WriteFile(path, []byte("# Taste\n\n## General\n\n- use early returns. Confidence: 0.90\n"), 0644)
	n, err := Reimport(db, path)
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
	n2, _ := Reimport(db, path)
	if n2 != 0 {
		t.Fatalf("reimport should be idempotent, got %d", n2)
	}
}
