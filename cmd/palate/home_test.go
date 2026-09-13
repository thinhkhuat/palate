package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/storage"
)

// runAllHooks drives the five hook handlers the way a harness does, against
// a real file in the cwd so post-tool-use has something to snapshot.
func runAllHooks(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("probe.txt", []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("probe.txt")
	runHook(t, `{"session_id":"h","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	runHook(t, `{"tool_input":{"file_path":"`+abs+`"}}`, "hook", "post-tool-use")
	runHook(t, `{"session_id":"h"}`, "hook", "stop")
	runHook(t, `{"prompt":"carry on"}`, "hook", "user-prompt")
	runHook(t, `{}`, "hook", "pre-compact")
}

// TestHooksUseGlobalHomeByDefault: with nothing pinning a repo, every hook
// keeps its state under <HOME>/.claude/palate/state and the cwd stays clean.
func TestHooksUseGlobalHomeByDefault(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	if cfg.Legacy() {
		t.Fatal("fresh cwd must resolve to the global home")
	}
	runAllHooks(t)

	if _, err := os.Stat(".palate"); !os.IsNotExist(err) {
		t.Fatal("global mode must not create .palate in the cwd")
	}
	for _, f := range []string{"model_edits.json", "recovery.md"} {
		if _, err := os.Stat(cfg.StatePath(f)); err != nil {
			t.Fatalf("expected %s under the home state dir: %v", f, err)
		}
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Fatalf("expected the DB under the home: %v", err)
	}
	home := os.Getenv("HOME")
	if !strings.HasPrefix(cfg.Home, filepath.Join(home, ".claude", "palate")) {
		t.Fatalf("home %q is not under HOME/.claude/palate", cfg.Home)
	}
	// seed-if-missing: a commented config lands beside the DB once
	if _, err := os.Stat(cfg.HomeConfigPath()); err != nil {
		t.Fatalf("expected seeded config.yaml in the home: %v", err)
	}
}

// TestHooksKeepLegacyRepoLayout: a cwd that already has .palate/palate.db is
// served exactly as 0.1.x did — state beside the DB, nothing in the home.
func TestHooksKeepLegacyRepoLayout(t *testing.T) {
	setupLegacyProject(t)
	cfg := loadConfig()
	if !cfg.Legacy() {
		t.Fatal("existing .palate/palate.db must pin legacy mode")
	}
	runAllHooks(t)

	for _, f := range []string{"model_edits.json", "recovery.md"} {
		if _, err := os.Stat(filepath.Join(".palate", f)); err != nil {
			t.Fatalf("legacy mode must keep %s in .palate: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude", "palate")); !os.IsNotExist(err) {
		t.Fatal("legacy mode must not touch the global home")
	}
	if _, err := os.Stat(filepath.Join(".palate", "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("legacy mode must not seed a config.yaml")
	}
}

// TestPalateHomeOverrideWinsInHooks: PALATE_HOME beats both detection and
// the default, and hooks follow it.
func TestPalateHomeOverrideWinsInHooks(t *testing.T) {
	setupLegacyProject(t) // even with a legacy marker present…
	override := t.TempDir()
	t.Setenv("PALATE_HOME", override)
	cfg := loadConfig()
	if cfg.Home != override || cfg.Legacy() {
		t.Fatalf("PALATE_HOME not honoured: home=%q legacy=%v", cfg.Home, cfg.Legacy())
	}
	runAllHooks(t)
	if _, err := os.Stat(filepath.Join(override, "state", "model_edits.json")); err != nil {
		t.Fatalf("state must land under PALATE_HOME/state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".palate", "model_edits.json")); !os.IsNotExist(err) {
		t.Fatal("PALATE_HOME set: .palate must not receive hook state")
	}
}

// TestSkillCandidatesLandInHome: skills helpers take the home, not the cwd.
func TestSkillCandidatesLandInHome(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	_ = openDB(cfg)
	if _, err := runCLI(t, "skill", "cluster"); err != nil {
		t.Fatalf("skill cluster: %v", err)
	}
	if _, err := os.Stat(".palate"); !os.IsNotExist(err) {
		t.Fatal("skill cluster must not create .palate in the cwd")
	}
	if _, err := runCLI(t, "skill", "stats", "some-skill"); err != nil {
		t.Fatalf("skill stats: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Home, "skill-stats.json")); err != nil {
		t.Fatalf("skill-stats.json must live in the home: %v", err)
	}
}

// TestDoctorReportsModeAndHome pins the user-facing proof of mode.
func TestDoctorReportsModeAndHome(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mode: global") || !strings.Contains(out, "home: "+loadConfig().Home) {
		t.Fatalf("doctor must print the home and mode, got:\n%s", out)
	}

	setupLegacyProject(t)
	out, _ = runCLI(t, "doctor")
	if !strings.Contains(out, "mode: legacy-repo") {
		t.Fatalf("doctor must report legacy-repo, got:\n%s", out)
	}
}

// TestMigrateMovesLegacyStoreLosslessly: `palate migrate` carries the
// legacy DB (tiers and confidences included) into the global home, parks
// the old directory under backups/, and refuses to clobber a global store
// that already holds events.
func TestMigrateMovesLegacyStoreLosslessly(t *testing.T) {
	setupLegacyProject(t)
	if _, err := runCLI(t, "prefer", "use table-driven tests"); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	legacyDB := loadConfig().DBPath
	if _, err := os.Stat(legacyDB); err != nil {
		t.Fatalf("legacy DB missing: %v", err)
	}

	out, err := runCLI(t, "migrate")
	if err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	storage.Close()
	if _, err := os.Stat(".palate"); !os.IsNotExist(err) {
		t.Fatal("migrate must park the legacy .palate/ directory")
	}
	global := loadConfig()
	if global.Legacy() {
		t.Fatal("after migrate the cwd must resolve to the global home")
	}
	db := openDB(global)
	var tier string
	var conf float64
	if err := db.QueryRow(`SELECT trust_tier, confidence FROM preferences WHERE statement='use table-driven tests'`).Scan(&tier, &conf); err != nil {
		t.Fatalf("preference did not survive the move: %v", err)
	}
	if tier != "STATED" || conf != 0.4 {
		t.Fatalf("tier/confidence changed in transit: %s %.2f", tier, conf)
	}
	storage.Close()
	if b, _ := os.ReadFile(global.TastePath()); !strings.Contains(string(b), "use table-driven tests") {
		t.Fatalf("TASTE.md not rendered in the global home:\n%s", b)
	}
	parked, _ := filepath.Glob(filepath.Join(global.BackupDir(), "legacy-*"))
	if len(parked) != 1 {
		t.Fatalf("expected the legacy dir parked under backups/, got %v", parked)
	}

	// a second legacy store must not clobber a global store that has data
	setupLegacyProject(t)
	t.Setenv("HOME", filepath.Dir(filepath.Dir(global.Home))) // same global home
	if _, err := runCLI(t, "prefer", "another repo's taste"); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	if _, err := runCLI(t, "migrate"); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("migrate into a populated global store must refuse, got err=%v", err)
	}
}
