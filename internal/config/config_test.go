package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func yamlUnmarshal(b []byte, v interface{}) error { return yaml.Unmarshal(b, v) }

func TestLLMConfigured(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     bool
	}{
		{"both empty", "", "", false},
		{"endpoint only", "https://api.example.com/v1/chat/completions", "", false},
		{"model only", "", "gpt", false},
		{"both set", "https://api.example.com/v1/chat/completions", "gpt", true},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.LLM.Endpoint = tc.endpoint
		cfg.LLM.Model = tc.model
		if got := cfg.LLMConfigured(); got != tc.want {
			t.Errorf("%s: LLMConfigured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDistillModeDefault(t *testing.T) {
	if got := Default().Distill.Mode; got != "auto" {
		t.Fatalf("Default().Distill.Mode = %q, want auto", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("db_path: .palate/palate.db\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Distill.Mode != "auto" {
		t.Fatalf("Load with no distill key: Mode = %q, want auto", cfg.Distill.Mode)
	}
}

// resolveIn runs Resolve with cwd, HOME and the PALATE_* env pinned so the
// precedence table below is hermetic on any machine.
func resolveIn(t *testing.T, cwd, home, palateHome, dbPath string) Config {
	t.Helper()
	t.Chdir(cwd)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	t.Setenv("PALATE_HOME", palateHome)
	t.Setenv("PALATE_DB_PATH", dbPath)
	cfg, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return cfg
}

func TestResolveHomePrecedence(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".claude", "palate")

	t.Run("default is the global durable home", func(t *testing.T) {
		cfg := resolveIn(t, t.TempDir(), home, "", "")
		if cfg.Home != global || cfg.Legacy() {
			t.Fatalf("Home=%q legacy=%v, want %q global", cfg.Home, cfg.Legacy(), global)
		}
		if cfg.DBPath != filepath.Join(global, "palate.db") {
			t.Fatalf("DBPath=%q", cfg.DBPath)
		}
		if cfg.StateDir() != filepath.Join(global, "state") || cfg.TastePath() != filepath.Join(global, "TASTE.md") {
			t.Fatalf("layout: state=%q taste=%q", cfg.StateDir(), cfg.TastePath())
		}
	})

	t.Run("PALATE_HOME wins", func(t *testing.T) {
		p := t.TempDir()
		cfg := resolveIn(t, t.TempDir(), home, p, "")
		if cfg.Home != p || cfg.Legacy() || cfg.DBPath != filepath.Join(p, "palate.db") {
			t.Fatalf("Home=%q legacy=%v db=%q", cfg.Home, cfg.Legacy(), cfg.DBPath)
		}
	})

	t.Run("home: in ./config.yaml wins over detection and default", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "config.yaml"), []byte("home: ./taste-home\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := resolveIn(t, cwd, home, "", "")
		if cfg.Home != filepath.Join(cwd, "taste-home") || cfg.Legacy() {
			t.Fatalf("Home=%q legacy=%v", cfg.Home, cfg.Legacy())
		}
	})

	t.Run("existing ./.palate/palate.db pins legacy repo mode", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cwd, ".palate"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, ".palate", "palate.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := resolveIn(t, cwd, home, "", "")
		want := filepath.Join(cwd, ".palate")
		if cfg.Home != want || !cfg.Legacy() {
			t.Fatalf("Home=%q legacy=%v, want %q legacy", cfg.Home, cfg.Legacy(), want)
		}
		// legacy keeps 0.1.x's layout: state beside the DB, not under state/
		if cfg.StateDir() != want || cfg.StatePath("model_edits.json") != filepath.Join(want, "model_edits.json") {
			t.Fatalf("legacy StateDir=%q", cfg.StateDir())
		}
		if cfg.DBPath != filepath.Join(want, "palate.db") {
			t.Fatalf("legacy DBPath=%q", cfg.DBPath)
		}
	})

	t.Run("explicit db_path is not overridden by the home", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "config.yaml"), []byte("db_path: ./elsewhere.db\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := resolveIn(t, cwd, home, t.TempDir(), "")
		if cfg.DBPath != filepath.Join(cwd, "elsewhere.db") {
			t.Fatalf("DBPath=%q (explicit db_path must be kept, anchored to the cwd)", cfg.DBPath)
		}
	})

	t.Run("PALATE_DB_PATH beats the derived path", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "explicit.db") // absolute on every OS
		cfg := resolveIn(t, t.TempDir(), home, t.TempDir(), explicit)
		if cfg.DBPath != explicit {
			t.Fatalf("DBPath=%q", cfg.DBPath)
		}
	})

	t.Run("relative PALATE_HOME is made absolute against the cwd once", func(t *testing.T) {
		cwd := t.TempDir()
		cfg := resolveIn(t, cwd, home, "rel-home", "")
		if !filepath.IsAbs(cfg.Home) || cfg.Home != filepath.Join(cwd, "rel-home") {
			t.Fatalf("Home=%q", cfg.Home)
		}
	})

	t.Run("tilde PALATE_HOME expands", func(t *testing.T) {
		cfg := resolveIn(t, t.TempDir(), home, "~/tilde-home", "")
		if cfg.Home != filepath.Join(home, "tilde-home") {
			t.Fatalf("Home=%q", cfg.Home)
		}
	})
}

func TestHomeConfigIsReadWhenRepoHasNone(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "ph")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "config.yaml"), []byte("c_floor: 0.42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := resolveIn(t, t.TempDir(), home, p, "")
	if cfg.CFloor != 0.42 {
		t.Fatalf("home config.yaml not applied: c_floor=%v", cfg.CFloor)
	}
	if cfg.Home != p {
		t.Fatalf("home config.yaml must not move the home: %q", cfg.Home)
	}
}

func TestEnsureLayoutSeedsConfigOnce(t *testing.T) {
	home := t.TempDir()
	cfg := resolveIn(t, t.TempDir(), home, "", "")
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cfg.Home, cfg.StateDir(), cfg.BackupDir()} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s: %v", d, err)
		}
	}
	seeded, err := os.ReadFile(cfg.HomeConfigPath())
	if err != nil {
		t.Fatalf("seed missing: %v", err)
	}
	if string(seeded) != string(seedYAML) {
		t.Fatal("seed content differs from the embedded default")
	}
	if err := os.WriteFile(cfg.HomeConfigPath(), []byte("c_floor: 0.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg.HomeConfigPath()); string(b) != "c_floor: 0.9\n" {
		t.Fatal("EnsureLayout overwrote a user-edited config.yaml")
	}

	// legacy mode never seeds a config into .palate
	cwd := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cwd, ".palate"), 0o700)
	_ = os.WriteFile(filepath.Join(cwd, ".palate", "palate.db"), nil, 0o600)
	lc := resolveIn(t, cwd, home, "", "")
	if err := lc.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".palate", "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("legacy mode must not seed .palate/config.yaml")
	}
}

// TestSeedMatchesDefaults pins the embedded seed.yaml to Default() so the
// two cannot drift apart silently.
// The seed is a fully commented template: as shipped it pins nothing (so a
// home seeded by an old version still follows new defaults), and every value
// it documents equals Default() once uncommented.
func TestSeedMatchesDefaults(t *testing.T) {
	var asShipped Config
	if err := yamlUnmarshal(seedYAML, &asShipped); err != nil {
		t.Fatalf("seed.yaml does not parse: %v", err)
	}
	if asShipped.CFloor != 0 || asShipped.HalfLifeHours != 0 || asShipped.TokenBudgets.PrewarmSession != 0 || asShipped.Weights != (Weights{}) {
		t.Fatalf("seed.yaml must not set any knob as shipped, got %+v", asShipped)
	}
	var uncommented []string
	for _, line := range strings.Split(string(seedYAML), "\n") {
		trim := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trim)]
		// "# key: value" → "key: value"; prose comments ("# Palate home …") stay comments.
		if strings.HasPrefix(trim, "# ") && strings.Contains(trim, ":") && !strings.Contains(strings.SplitN(trim[2:], ":", 2)[0], " ") {
			uncommented = append(uncommented, indent+trim[2:])
			continue
		}
		uncommented = append(uncommented, line)
	}
	var fromSeed Config
	if err := yamlUnmarshal([]byte(strings.Join(uncommented, "\n")), &fromSeed); err != nil {
		t.Fatalf("uncommented seed does not parse: %v\n%s", err, strings.Join(uncommented, "\n"))
	}
	d := Default()
	if fromSeed.CFloor != d.CFloor || fromSeed.HalfLifeHours != d.HalfLifeHours || fromSeed.Weights != d.Weights || fromSeed.TokenBudgets != d.TokenBudgets || fromSeed.Hybrid != d.Hybrid || fromSeed.Distill.Mode != d.Distill.Mode || len(fromSeed.Distill.SkipDirs) != 0 {
		t.Fatalf("seed.yaml (uncommented) drifted from Default():\nseed=%+v\ndef =%+v", fromSeed, d)
	}
}

func TestRenderAutoKnob(t *testing.T) {
	if !Default().Render.Auto {
		t.Fatal("render.auto must default to on")
	}
	home := t.TempDir()
	t.Setenv("PALATE_RENDER_AUTO", "0")
	cfg := resolveIn(t, t.TempDir(), home, "", "")
	if cfg.Render.Auto {
		t.Fatal("PALATE_RENDER_AUTO=0 must turn auto render off")
	}
}

func TestRelativeDBPathIsAnchoredToCwd(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "config.yaml"), []byte("db_path: ./x.db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := resolveIn(t, cwd, home, "", "")
	if cfg.DBPath != filepath.Join(cwd, "x.db") {
		t.Fatalf("relative db_path must be anchored to the cwd once: %q", cfg.DBPath)
	}
	cfg = resolveIn(t, cwd, home, "", "rel/env.db")
	if cfg.DBPath != filepath.Join(cwd, "rel", "env.db") {
		t.Fatalf("relative PALATE_DB_PATH must be anchored too: %q", cfg.DBPath)
	}
}

// TestConfigProfileDefaults pins the new profile.*/consolidate.* knobs and
// the prewarm_goal default flip (200 -> 0, phase-01 req 12).
func TestConfigProfileDefaults(t *testing.T) {
	d := Default()
	if !d.Profile.ExcludeProjectSpecific || !d.Profile.ReadBackEdits || !d.Profile.PromptBlock {
		t.Fatalf("profile defaults must all be on: %+v", d.Profile)
	}
	if d.Consolidate.TimeoutSeconds != 120 || d.Consolidate.StaleCandidateDays != 30 {
		t.Fatalf("consolidate defaults: %+v", d.Consolidate)
	}
	if d.TokenBudgets.PrewarmGoal != 0 {
		t.Fatalf("prewarm_goal default must be 0 (off), got %d", d.TokenBudgets.PrewarmGoal)
	}

	dir := t.TempDir()
	noKeys := filepath.Join(dir, "no-keys.yaml")
	if err := os.WriteFile(noKeys, []byte("c_floor: 0.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(noKeys)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Profile.ExcludeProjectSpecific || !cfg.Profile.ReadBackEdits || !cfg.Profile.PromptBlock {
		t.Fatalf("YAML without profile keys must keep defaults: %+v", cfg.Profile)
	}
	if cfg.Consolidate.TimeoutSeconds != 120 || cfg.Consolidate.StaleCandidateDays != 30 {
		t.Fatalf("YAML without consolidate keys must keep defaults: %+v", cfg.Consolidate)
	}

	explicit := filepath.Join(dir, "explicit.yaml")
	body := "profile:\n  exclude_project_specific: false\nconsolidate:\n  timeout_seconds: 0\n  stale_candidate_days: 0\ntoken_budgets:\n  prewarm_goal: 45\n"
	if err := os.WriteFile(explicit, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Profile.ExcludeProjectSpecific {
		t.Fatal("explicit false must be honoured")
	}
	if !cfg2.Profile.ReadBackEdits || !cfg2.Profile.PromptBlock {
		t.Fatalf("keys absent from the explicit document must still default true: %+v", cfg2.Profile)
	}
	if cfg2.Consolidate.TimeoutSeconds != 0 || cfg2.Consolidate.StaleCandidateDays != 0 {
		t.Fatalf("explicit 0 must be honoured: %+v", cfg2.Consolidate)
	}
	if cfg2.TokenBudgets.PrewarmGoal != 45 {
		t.Fatalf("explicit non-zero prewarm_goal must turn the goal block back on: %d", cfg2.TokenBudgets.PrewarmGoal)
	}
}

func TestDistillModeEnvAndSkipDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PALATE_DISTILL_MODE", "off")
	cfg := resolveIn(t, t.TempDir(), home, "", "")
	if cfg.LearningOn() {
		t.Fatal("PALATE_DISTILL_MODE=off must turn the automatic learner off")
	}
	if cfg.LearningMode() != "off" {
		t.Fatalf("LearningMode = %q, want off", cfg.LearningMode())
	}
	t.Setenv("PALATE_DISTILL_MODE", "bogus")
	cfg = resolveIn(t, t.TempDir(), home, "", "")
	if !cfg.LearningOn() || cfg.Distill.Mode != "auto" {
		t.Fatalf("an unknown mode must be ignored, got %q", cfg.Distill.Mode)
	}

	cfg.Distill.SkipDirs = []string{"~/ref", "/opt/clones/"}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	for cwd, want := range map[string]bool{
		home + "/ref":                  true,
		home + "/ref/some/repo":        true,
		home + "/reference":            false,
		"/opt/clones/x":                true,
		"/opt/clones":                  true,
		"/opt/clones-mine/y":           false,
		"":                             false,
		home + "/in-PROD/MY-WORKBENCH": false,
	} {
		if got := cfg.LearningSkipped(cwd); got != want {
			t.Fatalf("LearningSkipped(%q) = %v, want %v", cwd, got, want)
		}
	}
	if cfg.LearningMode() != "auto (2 skipped dirs)" {
		t.Fatalf("LearningMode = %q", cfg.LearningMode())
	}
}
