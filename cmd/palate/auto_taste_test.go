package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/render"
	"github.com/thinhkhuat/palate/internal/storage"
)

// tasteOnDisk reads the auto-maintained file; "" when absent.
func tasteOnDisk(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(loadConfig().TastePath())
	if err != nil {
		return ""
	}
	return string(b)
}

// freshRender is the oracle: what the DB says right now.
func freshRender(t *testing.T) string {
	t.Helper()
	cfg := loadConfig()
	db := openDB(cfg)
	out, err := render.Render(db, cfg, render.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAutoTasteFollowsEveryWrite: after prefer, forget, and a hook-captured
// stated preference, TASTE.md equals a fresh render of the DB.
func TestAutoTasteFollowsEveryWrite(t *testing.T) {
	setupTempProject(t)

	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatalf("prefer: %v", err)
	}
	storage.Close()
	got := tasteOnDisk(t)
	if !strings.Contains(got, "use table-driven tests") || !strings.Contains(got, "Confidence: 0.40") {
		t.Fatalf("TASTE.md not written after prefer:\n%s", got)
	}
	if want := freshRender(t); got != want {
		t.Fatalf("TASTE.md drifted from the DB after prefer:\n got: %s\nwant: %s", got, want)
	}
	storage.Close()

	runHook(t, `{"prompt":"I prefer early returns over nested ifs"}`, "hook", "user-prompt")
	got = tasteOnDisk(t)
	if !strings.Contains(got, "early returns") {
		t.Fatalf("TASTE.md not updated after the user-prompt hook:\n%s", got)
	}
	if want := freshRender(t); got != want {
		t.Fatalf("drift after hook:\n got: %s\nwant: %s", got, want)
	}
	storage.Close()

	out, err := runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	storage.Close()
	id := firstPreferenceID(t, out)
	if _, err := runCLI(t, "forget", id); err != nil {
		t.Fatalf("forget: %v", err)
	}
	storage.Close()
	got = tasteOnDisk(t)
	if want := freshRender(t); got != want {
		t.Fatalf("drift after forget:\n got: %s\nwant: %s", got, want)
	}
	storage.Close()
}

// firstPreferenceID pulls an id out of `palate export` output.
func firstPreferenceID(t *testing.T, export string) string {
	t.Helper()
	cfg := loadConfig()
	db := openDB(cfg)
	var id string
	if err := db.QueryRow(`SELECT id FROM preferences WHERE valid_until IS NULL LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("no preference to forget (export: %s): %v", export, err)
	}
	return id
}

// TestAutoRenderOff: PALATE_RENDER_AUTO=0 stops the writes entirely.
func TestAutoRenderOff(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_RENDER_AUTO", "0")
	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatalf("prefer: %v", err)
	}
	storage.Close()
	if _, err := os.Stat(loadConfig().TastePath()); !os.IsNotExist(err) {
		t.Fatal("render.auto off must not write TASTE.md")
	}
}

// TestAutoTasteUnchangedDBNoRewrite: a read-only verb leaves the file's
// mtime alone.
func TestAutoTasteUnchangedDBNoRewrite(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	path := loadConfig().TastePath()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "recall", "tests"); err != nil {
		t.Fatalf("recall: %v", err)
	}
	storage.Close()
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a read-only verb must not rewrite TASTE.md")
	}
}

// TestLintDefaultsToAutoFile: `palate lint` with no args lints the auto file.
func TestLintDefaultsToAutoFile(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	out, err := runCLI(t, "lint")
	if err != nil || !strings.Contains(out, "lint: ok") {
		t.Fatalf("lint on the auto file: err=%v out=%s", err, out)
	}
}

// TestBackupBeforeForgetAndPull: destructive verbs leave a dated copy of the
// DB in <home>/backups and doctor names the newest one.
func TestBackupBeforeForgetAndPull(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	id := firstPreferenceID(t, "")
	storage.Close()
	if _, err := runCLI(t, "forget", id); err != nil {
		t.Fatalf("forget: %v", err)
	}
	storage.Close()
	cfg := loadConfig()
	entries, _ := os.ReadDir(cfg.BackupDir())
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-forget") {
		t.Fatalf("forget must leave one backup, got %v", entries)
	}
	// pull takes an explicit path (the migrate one-liner relies on it)
	src := filepath.Join(t.TempDir(), "exported-TASTE.md")
	if err := os.WriteFile(src, []byte("# Taste\n\n## Testing\n\n- use golden files. Confidence: 0.40\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "pull", src); err != nil || !strings.Contains(out, "reimported 1 preferences") {
		t.Fatalf("pull <path>: err=%v out=%s", err, out)
	}
	storage.Close()
	entries, _ = os.ReadDir(cfg.BackupDir())
	if len(entries) != 2 {
		t.Fatalf("pull must add a second backup, got %v", entries)
	}
	// A one-session IMPORTED row stays a candidate under the stricter intake
	// rule (phase-01 eligibility: STATED or >= 2 distinct sessions) — it is
	// stored immediately, but TASTE.md only shows it once it is seen again.
	out, err := runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out, "use golden files") || !strings.Contains(out, `"trust_tier": "IMPORTED"`) {
		t.Fatalf("pull must store the imported row: %s", out)
	}
	storage.Close()
	out, _ = runCLI(t, "doctor")
	if !strings.Contains(out, "backup: newest ") {
		t.Fatalf("doctor must name the newest backup:\n%s", out)
	}
}

// TestAutoTasteAfterLearnSession: a distiller batch (fake gateway) also
// lands in TASTE.md — the detached child exits through storage.Close.
func TestAutoTasteAfterLearnSession(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "distill", "testdata", "claude_code_session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	setupTempProject(t)
	reply, _ := json.Marshal(map[string]interface{}{"preferences": []map[string]interface{}{
		{"statement": "Prefers table-driven tests.", "category": "Testing", "confidence": 0.9, "evidence": "always use table-driven tests"},
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(reply)}}}})
	}))
	defer srv.Close()
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")

	if out, err := runCLI(t, "learn", "session", "--harness", "claude_code", fixture); err != nil {
		t.Fatalf("learn session: %v\n%s", err, out)
	}
	storage.Close()
	// A one-session DISTILLED row is a candidate, not a profile member yet
	// (phase-01 eligibility needs STATED or >= 2 distinct sessions) — the
	// detached child still stores it and its auto-render still fires.
	out, err := runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out, "Prefers table-driven tests") || !strings.Contains(out, `"confidence": 0.9`) {
		t.Fatalf("learn session must store the distilled batch:\n%s", out)
	}
}
