package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// seedYAML is the shipped config, copied once into a fresh global home so the
// user has a commented file to edit (seed-if-missing, never overwritten).
//
//go:embed seed.yaml
var seedYAML []byte

// legacyDir is the per-repo layout Palate used before the durable home: a
// repo that already carries legacyDir/palate.db keeps that layout unchanged.
const legacyDir = ".palate"

type Config struct {
	// Home is the durable home every path Palate owns resolves under. Set by
	// ResolveHome: PALATE_HOME > `home:` in ./config.yaml > an existing
	// ./.palate/palate.db (legacy repo mode) > ~/.claude/palate.
	Home              string       `yaml:"home"`
	legacy            bool         // set by Resolve when Home came from legacy detection
	DBPath            string       `yaml:"db_path"`
	CFloor            float64      `yaml:"c_floor"`
	HalfLifeHours     float64      `yaml:"half_life_hours"`
	LearningRate      float64      `yaml:"learning_rate"`
	Weights           Weights      `yaml:"weights"`
	TokenBudgets      TokenBudgets `yaml:"token_budgets"`
	Freshness         Freshness    `yaml:"freshness"`
	Hybrid            Hybrid       `yaml:"hybrid"`
	TaxonomyThreshold int          `yaml:"taxonomy_threshold"`
	MCP               MCP          `yaml:"mcp"`
	// Rulebook is the path to the user's curated rulebook (e.g.
	// ~/.claude/CLAUDE.md). Learned rows that merely restate one of its rules
	// are tagged rulebook_overlap in provenance, hidden from TASTE.md and
	// retired by consolidation. Empty = the feature is off.
	Rulebook    string `yaml:"rulebook"`
	LLM         LLM
	Distill     Distill     `yaml:"distill"`
	Render      Render      `yaml:"render"`
	Judge       Judge       `yaml:"judge"`
	Consolidate Consolidate `yaml:"consolidate"`
	Profile     Profile     `yaml:"profile"`
}

// Profile controls the derived, budgeted TASTE.md/SessionStart profile
// (brainstorm-260927-1539 §6.1). ExcludeProjectSpecific hides project_specific
// -tagged rows from the profile; ReadBackEdits lets Phase 2's ReadBack promote
// the owner's own hand edits to TASTE.md into STATED preferences;
// PromptBlock gates the per-prompt block Phase 2 renders. All three exist so
// the owner can turn any one off without a code change.
type Profile struct {
	ExcludeProjectSpecific bool `yaml:"exclude_project_specific"`
	ReadBackEdits          bool `yaml:"read_back_edits"`
	PromptBlock            bool `yaml:"prompt_block"`
}

// Judge: when a gateway is configured the model judges every explicit write
// (statement, category, confidence, relation to existing rows). Off falls
// back to the heuristics. PALATE_JUDGE=0 overrides.
type Judge struct {
	Enabled bool `yaml:"enabled"`
}

// Consolidate: the whole-profile review pass. EveryEvents is the watermark
// distance that lets SessionStart auto-fire it (0 disables auto-fire).
// PALATE_CONSOLIDATE_EVERY overrides. TimeoutSeconds bounds one dream-pass run
// (Phase 4); StaleCandidateDays is the window a non-STATED candidate stays
// live before the dream pass forgets it for lack of repetition.
type Consolidate struct {
	EveryEvents        int `yaml:"every_events"`
	TimeoutSeconds     int `yaml:"timeout_seconds"`
	StaleCandidateDays int `yaml:"stale_candidate_days"`
}

// Render controls the auto-maintained TASTE.md. Auto is on unless the user
// turns it off in config.yaml or with PALATE_RENDER_AUTO=0.
type Render struct {
	Auto bool `yaml:"auto"`
}

type Weights struct {
	ExplicitCorrection float64 `yaml:"explicit_correction"`
	UserDiff           float64 `yaml:"user_diff"`
	ExplicitReject     float64 `yaml:"explicit_reject"`
	StatedPreference   float64 `yaml:"stated_preference"`
	ExplicitPrefer     float64 `yaml:"explicit_prefer"`
	ExplicitAccept     float64 `yaml:"explicit_accept"`
	ModelEdit          float64 `yaml:"model_edit"`
	Imported           float64 `yaml:"imported"`
}

type TokenBudgets struct {
	PrewarmSession int `yaml:"prewarm_session"`
	PrewarmGoal    int `yaml:"prewarm_goal"`
	Recall         int `yaml:"recall"`
	IndexLines     int `yaml:"index_lines"`
	IndexBytes     int `yaml:"index_bytes"`
}

type Freshness struct {
	Enabled       bool    `yaml:"enabled"`
	T1Days        int     `yaml:"t1_days"`
	T1Weight      float64 `yaml:"t1_weight"`
	T2Days        int     `yaml:"t2_days"`
	T2Weight      float64 `yaml:"t2_weight"`
	T3Weight      float64 `yaml:"t3_weight"`
	CompressChars int     `yaml:"compress_chars"`
}

type Hybrid struct {
	Vec        float64 `yaml:"vec"`
	FTS        float64 `yaml:"fts"`
	Importance float64 `yaml:"importance"`
	RRFK       int     `yaml:"rrf_k"`
}

type MCP struct {
	TokenEnv  string   `yaml:"token_env"`
	Allowlist []string `yaml:"allowlist"`
}

// LLM holds the chat-completion gateway settings. It carries no yaml tag: it
// is populated exclusively from PALATE_LLM_* env vars in applyEnv, never
// from config.yaml.
//
// TimeoutSeconds bounds ONE HTTP request to the gateway. It is a knob because
// the right value depends on the prompt size and the model: a whole-profile
// consolidation pass over a real (60+ row) store sends a much larger prompt
// than a per-statement judge call, and its generation is longer, so the old
// fixed 30 s made `palate consolidate` unreachable on a mature store
// (observed 2026-09-15: context deadline exceeded, no actions applied).
type LLM struct {
	Endpoint       string
	Model          string
	APIKey         string
	TimeoutSeconds int
}

// DefaultLLMTimeoutSeconds is the request timeout when none is configured.
const DefaultLLMTimeoutSeconds = 30

// LLMTimeout returns the configured per-request timeout, falling back to
// DefaultLLMTimeoutSeconds for a non-positive value.
func (c Config) LLMTimeout() time.Duration {
	s := c.LLM.TimeoutSeconds
	if s <= 0 {
		s = DefaultLLMTimeoutSeconds
	}
	return time.Duration(s) * time.Second
}

// Distill controls the automatic learner the hooks spawn. Mode is auto
// (write judged rows), review (write proposals) or off (never spawn it —
// Command Code's user-wide taste-learning switch); PALATE_DISTILL_MODE
// overrides. SkipDirs turns it off for any session whose cwd is under one of
// the listed directories (its per-project "disabled"): reference clones,
// other people's repos, anything you do not want mined. Explicit `palate
// learn …` commands ignore both — they are the user asking.
type Distill struct {
	Mode     string   `yaml:"mode"`
	SkipDirs []string `yaml:"skip_dirs"`
}

// LearningOn reports whether the hooks may spawn the automatic learner.
func (c Config) LearningOn() bool { return c.Distill.Mode != "off" }

// LearningSkipped reports whether cwd (a hook's working directory) lies under
// one of distill.skip_dirs. "" (unknown cwd) is never skipped. Paths are
// compared as cleaned strings, not resolved through symlinks: list a directory
// in the form the harness reports it.
func (c Config) LearningSkipped(cwd string) bool {
	if cwd == "" {
		return false
	}
	cwd = filepath.Clean(absHome(cwd))
	for _, d := range c.Distill.SkipDirs {
		if d == "" {
			continue
		}
		d = filepath.Clean(absHome(d))
		if cwd == d || strings.HasPrefix(cwd, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// LearningMode is the one-line description doctor prints.
func (c Config) LearningMode() string {
	mode := c.Distill.Mode
	switch mode {
	case "review", "off":
	default:
		mode = "auto"
	}
	if n := len(c.Distill.SkipDirs); n > 0 {
		return fmt.Sprintf("%s (%d skipped dir%s)", mode, n, map[bool]string{true: "s", false: ""}[n != 1])
	}
	return mode
}

func Default() Config {
	return Config{
		DBPath:        ".palate/palate.db",
		CFloor:        0.1,
		HalfLifeHours: 0, // decay off: confidence moves only on evidence (decisions row 18)
		LearningRate:  0.3,
		Weights: Weights{
			ExplicitCorrection: 1.0,
			UserDiff:           0.6,
			ExplicitReject:     0.9,
			StatedPreference:   0.8,
			ExplicitPrefer:     0.8,
			ExplicitAccept:     0.6,
			ModelEdit:          0.4,
			Imported:           0.4,
		},
		TokenBudgets: TokenBudgets{
			PrewarmSession: 2000, // profile budget, headers included; selection fills it with whole rows (decisions row 35)
			PrewarmGoal:    0,    // 0 = off (the per-prompt goal block is removed by default, §6.6); any value above 0 turns it back on
			Recall:         1000,
			IndexLines:     200,
			IndexBytes:     25600,
		},
		Freshness: Freshness{
			Enabled:       false,
			T1Days:        30,
			T1Weight:      1.0,
			T2Days:        90,
			T2Weight:      0.5,
			T3Weight:      0.25,
			CompressChars: 300,
		},
		Hybrid: Hybrid{
			Vec:        0.5,
			FTS:        0.3,
			Importance: 0.2,
			RRFK:       60,
		},
		TaxonomyThreshold: 5,
		MCP: MCP{
			TokenEnv:  "PALATE_MCP_TOKEN",
			Allowlist: []string{"127.0.0.1", "localhost", "::1", "ip6-localhost"},
		},
		Distill:     Distill{Mode: "auto"},
		Render:      Render{Auto: true},
		Judge:       Judge{Enabled: true},
		Consolidate: Consolidate{EveryEvents: 25, TimeoutSeconds: 300, StaleCandidateDays: 30},
		Profile:     Profile{ExcludeProjectSpecific: true, ReadBackEdits: true, PromptBlock: true},
	}
}

// DefaultHome is the canonical durable home shared by every harness
// (Claude Code, Codex, Command Code, ZCode all read the same store) — the
// same placement skill-concierge uses (~/.claude/skill-concierge).
func DefaultHome() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return filepath.Join(".", legacyDir)
	}
	return filepath.Join(h, ".claude", "palate")
}

// ResolveHome is the one place the home is decided. yamlHome is the `home:`
// value read from ./config.yaml ("" when absent). Precedence: PALATE_HOME >
// yamlHome > legacy detection (./.palate/palate.db exists) > DefaultHome.
// A relative value is made absolute against the cwd once, here, so hooks that
// run later with a different cwd still address the same directory.
func ResolveHome(yamlHome string) (home string, legacy bool) {
	if v := os.Getenv("PALATE_HOME"); v != "" {
		return absHome(v), false
	}
	if yamlHome != "" {
		return absHome(yamlHome), false
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "palate.db")); err == nil {
		return absHome(legacyDir), true
	}
	return DefaultHome(), false
}

func absHome(p string) string {
	// Only the current user's "~" / "~/x" forms; "~other/x" is left alone.
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = h + p[1:]
		}
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// ForGlobalHome returns base re-pointed at the global home (PALATE_HOME or
// DefaultHome) with the legacy flag cleared and the DB derived from it —
// what a legacy repo would resolve to once its ./.palate/ is gone.
func ForGlobalHome(base Config) Config {
	c := base
	c.legacy = false
	if v := os.Getenv("PALATE_HOME"); v != "" {
		c.Home = absHome(v)
	} else {
		c.Home = DefaultHome()
	}
	c.DBPath = filepath.Join(c.Home, "palate.db")
	return c
}

// Legacy reports the per-repo layout (Home is the cwd's .palate, chosen by
// detection, not by the user).
func (c Config) Legacy() bool { return c.legacy }

// StateDir holds hook state (model_edits.json, recovery.md, learn.lock):
// <home>/state in global mode, the home itself in legacy mode (where 0.1.x
// left it).
func (c Config) StateDir() string {
	if c.Legacy() {
		return c.Home
	}
	return filepath.Join(c.Home, "state")
}

func (c Config) StatePath(name string) string { return filepath.Join(c.StateDir(), name) }
func (c Config) TastePath() string            { return filepath.Join(c.Home, "TASTE.md") }
func (c Config) BackupDir() string            { return filepath.Join(c.Home, "backups") }
func (c Config) SkillsDir() string            { return filepath.Join(c.Home, "skills") }
func (c Config) SkillCandidatesDir() string   { return filepath.Join(c.Home, "skill-candidates") }
func (c Config) HomeConfigPath() string       { return filepath.Join(c.Home, "config.yaml") }

// EnsureLayout creates the home, its state and backups dirs (0700) and, in
// global mode, seeds config.yaml from the shipped default when absent. It
// never overwrites anything. Idempotent and cheap; every DB open calls it.
func (c Config) EnsureLayout() error {
	for _, d := range []string{c.Home, c.StateDir(), c.BackupDir()} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	if c.Legacy() {
		return nil
	}
	if _, err := os.Stat(c.HomeConfigPath()); os.IsNotExist(err) {
		return os.WriteFile(c.HomeConfigPath(), seedYAML, 0600)
	}
	return nil
}

// Resolve loads the effective config: `explicit` (the --config flag, or
// "config.yaml" meaning ./config.yaml) first; when that file is absent and
// the home is not the legacy layout, <home>/config.yaml; then defaults. The
// home is resolved through ResolveHome and the DB path is derived from it
// unless the user set db_path / PALATE_DB_PATH themselves. Both the CLI and
// the MCP server go through here so they can never disagree on the store.
func Resolve(explicit string) (Config, error) {
	if explicit == "" {
		explicit = "config.yaml"
	}
	cfg, found, err := loadFile(explicit)
	if err != nil {
		return cfg, err
	}
	cfg.Home, cfg.legacy = ResolveHome(cfg.Home)
	if !found && !cfg.legacy {
		if hc, hfound, herr := loadFile(cfg.HomeConfigPath()); herr == nil && hfound {
			home := cfg.Home
			cfg = hc
			cfg.Home = home
		}
	}
	if cfg.DBPath == Default().DBPath {
		cfg.DBPath = filepath.Join(cfg.Home, "palate.db")
	}
	applyEnv(&cfg)
	// A leading ~/ resolves against the user's home so the rulebook path in
	// config.yaml reads the way every shell expands it.
	cfg.Rulebook = expandHomePath(cfg.Rulebook)
	// An explicit relative db_path (yaml or PALATE_DB_PATH) is anchored to the
	// cwd once, here, for the same reason the home is: hooks and the detached
	// distiller must address the same file whatever their cwd.
	if cfg.DBPath != ":memory:" {
		cfg.DBPath = absHome(cfg.DBPath)
	}
	return cfg, nil
}

// expandHomePath resolves a leading ~/ against the user's home directory so
// a rulebook path in config.yaml reads the way every shell expands it. A
// path without the prefix passes through unchanged; an unresolvable home
// returns the path as-is (the loader will then fail-open).
func expandHomePath(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// loadFile reads one YAML file over Default() without touching env or the
// home; found reports whether the file existed.
func loadFile(path string) (Config, bool, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, true, err
	}
	return cfg, true, nil
}

// Load is Resolve for callers that hold an explicit config path.
func Load(path string) (Config, error) { return Resolve(path) }

func applyEnv(c *Config) {
	if v := os.Getenv("PALATE_DB_PATH"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("PALATE_C_FLOOR"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.CFloor = f
		}
	}
	if v := os.Getenv("PALATE_HALF_LIFE_HOURS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.HalfLifeHours = f
		}
	}
	if v := os.Getenv("PALATE_LLM_ENDPOINT"); v != "" {
		c.LLM.Endpoint = v
	}
	if v := os.Getenv("PALATE_LLM_MODEL"); v != "" {
		c.LLM.Model = v
	}
	if v := os.Getenv("PALATE_LLM_API_KEY"); v != "" {
		c.LLM.APIKey = v
	}
	if v := os.Getenv("PALATE_LLM_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LLM.TimeoutSeconds = n
		}
	}
	if v := os.Getenv("PALATE_RENDER_AUTO"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Render.Auto = b
		}
	}
	if v := os.Getenv("PALATE_DISTILL_MODE"); v != "" {
		switch v {
		case "auto", "review", "off":
			c.Distill.Mode = v
		}
	}
	if v := os.Getenv("PALATE_JUDGE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Judge.Enabled = b
		}
	}
	if v := os.Getenv("PALATE_CONSOLIDATE_EVERY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Consolidate.EveryEvents = n
		}
	}
}

// JudgeIsLLM reports whether explicit writes are judged by the gateway.
func (c Config) JudgeIsLLM() bool { return c.LLMConfigured() && c.Judge.Enabled }

// LLMConfigured reports whether enough of PALATE_LLM_* is set to attempt a
// gateway call. The API key is optional (some gateways need none).
func (c Config) LLMConfigured() bool {
	return c.LLM.Endpoint != "" && c.LLM.Model != ""
}

func (c Config) WeightFor(signal string) float64 {
	switch signal {
	case "explicit_correction":
		return c.Weights.ExplicitCorrection
	case "user_diff":
		return c.Weights.UserDiff
	case "explicit_reject":
		return c.Weights.ExplicitReject
	case "stated_preference":
		return c.Weights.StatedPreference
	case "explicit_prefer":
		return c.Weights.ExplicitPrefer
	case "explicit_accept":
		return c.Weights.ExplicitAccept
	case "model_edit":
		return c.Weights.ModelEdit
	case "imported":
		return c.Weights.Imported
	default:
		return c.Weights.StatedPreference
	}
}
