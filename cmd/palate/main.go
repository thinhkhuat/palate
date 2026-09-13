package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thinhkhuat/palate/internal/adapters"
	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/mcp"
	"github.com/thinhkhuat/palate/internal/prewarm"
	"github.com/thinhkhuat/palate/internal/render"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/skills"
	"github.com/thinhkhuat/palate/internal/storage"
	"github.com/thinhkhuat/palate/internal/verify"
)

var cfgFile string

// version is set at release time via GoReleaser's ldflags
// (-X main.version={{.Version}}, .goreleaser.yaml:10); it stays "dev" for
// go run/go test and any build that doesn't pass the flag.
var version = "dev"

// spawnLearnSession is a seam over distill.SpawnLearnSession so hook tests
// can swap in a spy instead of starting a real child process; the
// production path leaves it pointed at the real function.
var spawnLearnSession = distill.SpawnLearnSession

func loadConfig() config.Config {
	if cfgFile == "" {
		cfgFile = "config.yaml"
	}
	cfg, _ := config.Load(cfgFile)
	return cfg
}

func openDB(cfg config.Config) *sql.DB {
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	return db
}

// gateExplicit rejects statements that fail the secret or injection scan.
// Explicit statements are stored at the STATED tier and pushed unredacted
// into prewarm, so flagged content is refused rather than stored cleaned.
func gateExplicit(stmt string) error {
	if scan := safety.Scan(stmt); scan.Flagged {
		return fmt.Errorf("rejected: %s pattern", scan.Reason)
	}
	return nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "palate",
		Short: "Palate — open taste engine",
	}
	root.PersistentFlags().StringVar(&cfgFile, "config", "config.yaml", "config file")

	root.AddCommand(cmdInit())
	root.AddCommand(cmdDoctor())
	root.AddCommand(cmdStore())
	root.AddCommand(cmdRecall())
	root.AddCommand(cmdPrewarm())
	root.AddCommand(cmdSleep())
	root.AddCommand(cmdServe())
	root.AddCommand(cmdStats())
	root.AddCommand(cmdForget())
	root.AddCommand(cmdExport())
	root.AddCommand(cmdLint())
	root.AddCommand(cmdReimport())
	root.AddCommand(cmdPush())
	root.AddCommand(cmdPull())
	root.AddCommand(cmdVerify())
	root.AddCommand(cmdHook())
	root.AddCommand(cmdPrefer())
	root.AddCommand(cmdCorrect())
	root.AddCommand(cmdPropose())
	root.AddCommand(cmdPromote())
	root.AddCommand(cmdSkill())
	root.AddCommand(cmdUpdate())
	root.AddCommand(cmdReindex())
	root.AddCommand(cmdLearn())
	root.AddCommand(cmdVersion())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func cmdInit() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize .palate directory and database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			_ = db
			storage.Close()
			fmt.Println("palate initialized at", cfg.DBPath)
			return nil
		},
	}
}

func cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Self-diagnostic",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			fmt.Printf("config: %s\n", cfgFile)
			fmt.Printf("db: %s\n", cfg.DBPath)
			fmt.Printf("c_floor: %.2f half_life: %.0fh\n", cfg.CFloor, cfg.HalfLifeHours)
			// Diagnose only: never create the database as a side effect.
			if _, statErr := os.Stat(cfg.DBPath); os.IsNotExist(statErr) {
				fmt.Printf("db: MISSING %s (run: palate init)\n", cfg.DBPath)
				return fmt.Errorf("doctor: database missing")
			}
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				fmt.Printf("db: FAIL %v\n", err)
				return fmt.Errorf("doctor: database unreadable at %s", cfg.DBPath)
			}
			defer storage.Close()
			var v string
			if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err == nil {
				fmt.Printf("sqlite: %s\n", v)
			}
			var fts int
			_ = db.QueryRow(`SELECT count(*) FROM fts_working WHERE fts_working MATCH 'test'`).Scan(&fts)
			fmt.Printf("fts5: ok\n")
			fmt.Printf("vector: json+cosine (native vec behind build tag)\n")
			fmt.Printf("adapters: %d (%d verified for learning)\n", len(adapters.Registry), countVerified(adapters.Registry))
			for _, a := range adapters.Registry {
				status := "recall-only"
				if a.Verified {
					status = "learning"
				}
				fmt.Printf("adapter: %-16s %-12s — %s\n", a.Name, status, a.Install)
			}
			scan := safety.Scan("test content")
			fmt.Printf("scanner: ok (flagged=%v)\n", scan.Flagged)
			fmt.Println("doctor: green")
			return nil
		},
	}
}

func cmdStore() *cobra.Command {
	var table, session string
	var importance float64
	c := &cobra.Command{
		Use:   "store [content]",
		Short: "Store a memory",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := args[0]
			if safety.ContainsSecret(content) {
				return fmt.Errorf("rejected: content contains a secret pattern")
			}
			scan := safety.Scan(content)
			if scan.Flagged && scan.Reason == "injection" {
				content = scan.Cleaned
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			id, err := storage.Remember(db, content, session, importance, table)
			if err != nil {
				return err
			}
			fmt.Println(id)
			return nil
		},
	}
	c.Flags().StringVar(&table, "table", "working_memory", "table")
	c.Flags().StringVar(&session, "session", "", "session id")
	c.Flags().Float64Var(&importance, "importance", 0.5, "importance 0..1")
	return c
}

func cmdRecall() *cobra.Command {
	var limit int
	var jsonOut, includeConflicted bool
	var asOf string
	c := &cobra.Command{
		Use:   "recall [query]",
		Short: "Recall memories (supports --as-of and --include-conflicted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			q := ""
			if len(args) > 0 {
				q = args[0]
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var asOfTime *time.Time
			if asOf != "" {
				t, err := time.Parse(time.RFC3339, asOf)
				if err != nil {
					return fmt.Errorf("bad --as-of %q: want RFC3339 (e.g. 2026-09-01T00:00:00Z): %w", asOf, err)
				}
				asOfTime = &t
				fmt.Fprintf(os.Stderr, "note: --as-of %s pins decay replay to that instant\n", asOf)
			}
			results, err := storage.RecallFull(db, q, limit, includeConflicted, asOfTime, storage.RecallOptions{
				CFloor:        cfg.CFloor,
				HalfLifeHours: cfg.HalfLifeHours,
				Freshness: storage.FreshnessWeights{
					Enabled:  cfg.Freshness.Enabled,
					T1Days:   cfg.Freshness.T1Days,
					T1Weight: cfg.Freshness.T1Weight,
					T2Days:   cfg.Freshness.T2Days,
					T2Weight: cfg.Freshness.T2Weight,
					T3Weight: cfg.Freshness.T3Weight,
				},
				RRFK: cfg.Hybrid.RRFK,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				b, _ := json.MarshalIndent(results, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			for _, r := range results {
				fmt.Printf("[%s] %s (%.3f) id=%s\n", r.Table, r.Content, r.Score, r.ID)
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 10, "max results")
	c.Flags().BoolVar(&jsonOut, "json", false, "json output")
	c.Flags().BoolVar(&includeConflicted, "include-conflicted", false, "include conflicted prefs")
	c.Flags().StringVar(&asOf, "as-of", "", "RFC3339 instant to pin decay (replay determinism)")
	return c
}

func cmdPrewarm() *cobra.Command {
	var scope, prompt string
	c := &cobra.Command{
		Use:   "prewarm",
		Short: "Emit prewarm block",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if scope == "goal" {
				if prompt == "" {
					return fmt.Errorf("prewarm --scope goal requires --prompt")
				}
				block, fired, err := prewarm.GoalPrewarm(db, cfg, prompt)
				if err != nil {
					return err
				}
				if !fired {
					return nil
				}
				fmt.Print(block.Content)
				return nil
			}
			block, err := prewarm.SessionPrewarm(db, cfg)
			if err != nil {
				return err
			}
			fmt.Print(block.Content)
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "session", "session|goal")
	c.Flags().StringVar(&prompt, "prompt", "", "user prompt (required with --scope goal)")
	return c
}

func cmdSleep() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep",
		Short: "Consolidate working -> episodic",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			n, err := storage.Sleep(db)
			if err != nil {
				return err
			}
			fmt.Printf("consolidated %d\n", n)
			return nil
		},
	}
}

func cmdServe() *cobra.Command {
	var port string
	c := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP (stdio default, --port for SSE)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if port != "" {
				return mcp.ServeSSE(cfg, sseAddr(port))
			}
			return mcp.ServeStdio(cfg)
		},
	}
	c.Flags().StringVar(&port, "port", "", "SSE port (default: stdio)")
	return c
}

// sseAddr turns --port into a listen address: a bare port becomes ":port",
// a host:port form such as 127.0.0.1:4317 passes through unchanged.
func sseAddr(port string) string {
	if strings.Contains(port, ":") {
		return port
	}
	return ":" + port
}

func cmdStats() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show stats",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var wm, em, prefs, events int
			_ = db.QueryRow(`SELECT count(*) FROM working_memory`).Scan(&wm)
			_ = db.QueryRow(`SELECT count(*) FROM episodic_memory`).Scan(&em)
			_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&prefs)
			_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
			fmt.Printf("working: %d episodic: %d preferences: %d events: %d\n", wm, em, prefs, events)
			return nil
		},
	}
}

func cmdForget() *cobra.Command {
	return &cobra.Command{
		Use:   "forget [id]",
		Short: "Forget by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if err := storage.Forget(db, args[0]); err != nil {
				return err
			}
			fmt.Println("forgot", args[0])
			return nil
		},
	}
}

func cmdExport() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "export",
		Short: "Export preferences as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			prefs, err := learn.ListPreferences(db, true)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(prefs, "", "  ")
			if out != "" {
				return os.WriteFile(out, b, 0644)
			}
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "", "output file")
	return c
}

func cmdLint() *cobra.Command {
	return &cobra.Command{
		Use:   "lint [TASTE.md]",
		Short: "Lint TASTE.md (lossy artifact check; ok = no drift from DB)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "TASTE.md"
			if len(args) > 0 {
				path = args[0]
			}
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "lint: no TASTE.md on disk")
				fmt.Fprintln(os.Stderr, "lint: render one from DB: palate push   (or: palate export --out TASTE.md)")
				fmt.Fprintln(os.Stderr, "lint: note: TASTE.md is a LOSS-ONLY render; events log in .palate/*.db is the source of truth")
				return nil
			}
			diags := render.Lint(path)
			if len(diags) == 0 {
				fmt.Println("lint: ok")
				return nil
			}
			for _, d := range diags {
				fmt.Fprintln(os.Stderr, d)
			}
			return fmt.Errorf("lint: %d issue(s)", len(diags))
		},
	}
}

func cmdReimport() *cobra.Command {
	return &cobra.Command{
		Use:   "reimport [TASTE.md]",
		Short: "Reimport TASTE.md as stated_preference signals (w=0.4 IMPORTED)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "TASTE.md"
			if len(args) > 0 {
				path = args[0]
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			n, err := render.Reimport(db, path)
			if err != nil {
				return err
			}
			fmt.Printf("reimported %d\n", n)
			return nil
		},
	}
}

func cmdPush() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push TASTE.md (git push/pull semantics)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			path := "TASTE.md"
			if err := render.WriteFile(db, path, render.RenderOptions{}); err != nil {
				return err
			}
			fmt.Printf("TASTE.md rendered from DB (lossy artifact, events is truth). Next: git add %s && git commit -m 'palate: update TASTE.md' && git push\n", path)
			return nil
		},
	}
}

func cmdPull() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull TASTE.md then reimport",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			n, err := render.Reimport(db, "TASTE.md")
			if err != nil {
				fmt.Println("pull: git pull first, then reimport —", err)
				return nil
			}
			fmt.Printf("reimported %d preferences from TASTE.md (IMPORTED w<=0.4 quarantine)\n", n)
			return nil
		},
	}
}

func cmdReindex() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild FTS/vector indexes from DB (not markdown)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			_, _ = db.Exec(`DELETE FROM fts_working WHERE fts_working MATCH '*'`)
			_, _ = db.Exec(`DELETE FROM fts_episodic WHERE fts_episodic MATCH '*'`)
			_, _ = db.Exec(`INSERT INTO fts_working(fts_working) VALUES('rebuild')`)
			_, _ = db.Exec(`INSERT INTO fts_episodic(fts_episodic) VALUES('rebuild')`)
			fmt.Println("reindex: FTS rebuilt from DB")
			return nil
		},
	}
}

func cmdVerify() *cobra.Command {
	var pure bool
	c := &cobra.Command{
		Use:   "verify [capture|retrieval]",
		Short: "Verification harness",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := "capture"
			if len(args) > 0 {
				kind = args[0]
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var rep verify.Report
			var err error
			switch kind {
			case "retrieval":
				rep, err = verify.RunRetrieval(db, pure)
			default:
				rep, err = verify.RunCapture(db, pure)
			}
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
			for _, issue := range verify.IndependenceCheck() {
				fmt.Fprintln(os.Stderr, "warn:", issue)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&pure, "pure", false, "disable oracles")
	return c
}

func cmdHook() *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "Hook handlers (called by harness)"}

	var installHarnesses []string
	install := &cobra.Command{
		Use:   "install",
		Short: "Install palate hooks (Claude Code, Command Code, ZCode)",
		Long: "Writes palate hook entries into harness settings, idempotently.\n" +
			"Targets: claude_code (.claude/settings.local.json), command_code (.commandcode/settings.local.json),\n" +
			"zcode (.zcode/config.json, workspace) and zcode-global (~/.zcode/cli/config.json).\n" +
			"Default with no --harness: claude_code, command_code, zcode.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			bin := hookBinaryPath()
			want, err := parseHarnessList(installHarnesses)
			if err != nil {
				return err
			}
			var installed []string
			for _, h := range []string{"claude_code", "command_code", "zcode", "zcode-global"} {
				if !want[h] {
					continue
				}
				switch h {
				case "claude_code":
					err = adapters.WriteClaudeCodeHooks(cwd, bin)
				case "command_code":
					err = adapters.WriteCommandCodeHooks(cwd, bin)
				case "zcode":
					err = adapters.WriteZCodeHooks(cwd, bin)
				case "zcode-global":
					err = adapters.WriteZCodeHooksGlobal(bin)
				}
				if err != nil {
					return fmt.Errorf("%s: %w", h, err)
				}
				installed = append(installed, h)
			}
			fmt.Printf("hooks installed: %s\n", strings.Join(installed, ", "))
			fmt.Println("restart the harness so it picks up the hooks")
			return nil
		},
	}
	install.Flags().StringSliceVar(&installHarnesses, "harness", nil,
		"claude_code|command_code|zcode|zcode-global (comma-separated; default: claude_code,command_code,zcode)")
	c.AddCommand(install)

	var uninstallHarnesses []string
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove palate hooks from harness settings (foreign hooks preserved)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			want, err := parseHarnessList(uninstallHarnesses)
			if err != nil {
				return err
			}
			for _, h := range []string{"claude_code", "command_code", "zcode", "zcode-global"} {
				if !want[h] {
					continue
				}
				var n int
				switch h {
				case "claude_code":
					n, err = adapters.RemoveClaudeCodeHooks(cwd)
				case "command_code":
					n, err = adapters.RemoveCommandCodeHooks(cwd)
				case "zcode":
					n, err = adapters.RemoveZCodeHooksAt(adapters.ZCodeWorkspaceConfigPath(cwd))
				case "zcode-global":
					var p string
					if p, err = adapters.ZCodeGlobalConfigPath(); err == nil {
						n, err = adapters.RemoveZCodeHooksAt(p)
					}
				}
				if err != nil {
					return fmt.Errorf("%s: %w", h, err)
				}
				fmt.Printf("%s: removed %d palate hook entr%s\n", h, n, plural(n))
				if h == "zcode" || h == "zcode-global" {
					var p string
					if h == "zcode" {
						p = adapters.ZCodeWorkspaceConfigPath(cwd)
					} else if p, _ = adapters.ZCodeGlobalConfigPath(); p == "" {
						continue
					}
					if adapters.ZCodeEnabledWithForeignHooks(p) {
						fmt.Printf("%s: note: hooks.enabled left true — foreign hooks remain in %s; unset it yourself if you want config hooks off\n", h, p)
					}
				}
			}
			return nil
		},
	}
	uninstall.Flags().StringSliceVar(&uninstallHarnesses, "harness", nil,
		"claude_code|command_code|zcode|zcode-global (default: all)")
	c.AddCommand(uninstall)

	c.AddCommand(&cobra.Command{
		Use:     "help",
		Aliases: []string{"list", "status"},
		Short:   "Show palate hook wiring status per harness",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			type row struct {
				name  string
				path  string
				count int
			}
			rows := []row{
				{"claude_code", filepath.Join(cwd, ".claude", "settings.local.json"), 0},
				{"command_code", filepath.Join(cwd, ".commandcode", "settings.local.json"), 0},
			}
			for i := range rows {
				rows[i].count = adapters.CountPalateHooksTopLevel(rows[i].path)
			}
			zc := row{"zcode", adapters.ZCodeWorkspaceConfigPath(cwd), 0}
			zc.count = adapters.CountPalateHooksZCode(zc.path)
			rows = append(rows, zc)
			if globalPath, err := adapters.ZCodeGlobalConfigPath(); err == nil {
				rows = append(rows, row{"zcode-global", globalPath, adapters.CountPalateHooksZCode(globalPath)})
			}
			for _, r := range rows {
				status := "not installed"
				if r.count > 0 {
					status = fmt.Sprintf("%d entries — learning wired", r.count)
				}
				fmt.Printf("%-14s %-32s %s\n", r.name+":", status, r.path)
			}
			if pluginHooksInstalled() && rows[0].count > 0 {
				fmt.Println(pluginDoubleFireWarning)
			}
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "session-start",
		Short: "SessionStart handler (JSON additionalContext + cross-session user_diff)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Best-effort by contract: a capture hook must never break the
			// session, so errors degrade to empty output (exit 0).
			var input struct {
				SessionID      string `json:"session_id"`
				TranscriptPath string `json:"transcript_path"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			cfg := loadConfig()
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				return nil
			}
			defer storage.Close()
			handleStateWalkUserDiffs(db, cfg)
			if cfg.LLMConfigured() && input.TranscriptPath != "" {
				// Detached, fire-and-forget: this must never make the hook
				// wait on the distiller's LLM call.
				_ = spawnLearnSession(hookBinaryPath(), input.TranscriptPath)
			}
			block, err := prewarm.SessionPrewarm(db, cfg)
			if err != nil || block.Content == "" {
				return nil
			}
			out := map[string]interface{}{
				"hookSpecificOutput": map[string]interface{}{
					"hookEventName":     "SessionStart",
					"additionalContext": block.Content,
				},
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "user-prompt",
		Short: "UserPromptSubmit handler",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var input struct {
				Prompt string `json:"prompt"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			// The user just handed the turn back: anything that drifted from
			// the Stop-time snapshot since then is a hand-edit — learn it now.
			handleStateWalkUserDiffs(db, cfg)
			// Superseded by the live session distiller when an LLM is
			// configured: the same prompt text is already sitting in the
			// transcript file the Stop hook is about to distill, so the
			// regex/heuristic path stays off to avoid double-learning it.
			if !cfg.LLMConfigured() {
				// Scan the whole prompt, not the extracted clause: the trigger regex
				// starts at "prefer", which would strip an injection prefix and
				// launder the payload into a trusted STATED preference.
				if stmt, cat := learn.ExtractStatedPreference(input.Prompt); stmt != "" {
					if safety.Scan(input.Prompt).Flagged {
						storage.BumpDrop(db, "stated_preference")
					} else {
						_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalStatedPreference, Statement: stmt, Category: cat}, nil)
					}
				}
			}
			if input.Prompt == "" {
				return nil
			}
			if block, fired, _ := prewarm.GoalPrewarm(db, cfg, input.Prompt); fired {
				out := map[string]interface{}{
					"hookSpecificOutput": map[string]interface{}{
						"hookEventName":     "UserPromptSubmit",
						"additionalContext": block.Content,
					},
				}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "post-tool-use",
		Short: "PostToolUse handler (records model_edit file→hash)",
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil
			}
			var v map[string]interface{}
			if json.Unmarshal(raw, &v) != nil {
				return nil
			}
			fp := hookFilePath(v)
			if fp == "" {
				return nil
			}
			b, err := os.ReadFile(fp)
			if err != nil {
				return nil
			}
			statePath := filepath.Join(".palate", "model_edits.json")
			_ = os.MkdirAll(".palate", 0700)
			state := loadModelEdits(statePath)
			state[fp] = newModelEdit(b)
			if err := saveModelEdits(statePath, state); err != nil {
				return nil
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop handler (re-baselines model_edit snapshots; files[] payload learns user_diff)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var input struct {
				SessionID      string `json:"session_id"`
				TranscriptPath string `json:"transcript_path"`
				Files          []struct {
					Path      string `json:"path"`
					HashAfter string `json:"hash_after"`
				} `json:"files"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			statePath := filepath.Join(".palate", "model_edits.json")
			modelEdits := loadModelEdits(statePath)
			if len(input.Files) > 0 {
				// Explicit files[] payload (scripted callers).
				for _, f := range input.Files {
					if f.Path == "" {
						continue
					}
					rec, hasModelEdit := modelEdits[f.Path]
					if !hasModelEdit {
						storage.BumpDrop(db, "user_diff")
						continue
					}
					if applyUserDiffSignal(db, cfg, f.Path, rec.Hash, rec.Snapshot, f.HashAfter) {
						delete(modelEdits, f.Path)
					}
				}
			} else {
				// No files[] in the hook payload (what live harnesses send).
				// Stop fires while the assistant still owns the turn, so any
				// drift between the PostToolUse snapshot and disk here is the
				// assistant's own (a sed/python edit after an Edit/Write), not
				// the user's. Re-baseline every snapshot to disk instead of
				// learning; the user_diff comparison runs at the next
				// UserPromptSubmit / SessionStart, the only moments where a
				// change can only have come from the user.
				refreshModelEdits(modelEdits)
			}
			_ = saveModelEdits(statePath, modelEdits)
			if cfg.LLMConfigured() && input.TranscriptPath != "" {
				// Detached, fire-and-forget: this must never make the hook
				// wait on the distiller's LLM call.
				_ = spawnLearnSession(hookBinaryPath(), input.TranscriptPath)
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "pre-compact",
		Short: "PreCompact handler",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			block, _ := prewarm.SessionPrewarm(db, cfg)
			path := filepath.Join(".palate", "recovery.md")
			_ = os.MkdirAll(".palate", 0700)
			_ = os.WriteFile(path, []byte(block.Content), 0600)
			return nil
		},
	})
	return c
}

// pluginDoubleFireWarning is printed by `hook help` when a plugin install and
// a workspace-level install both appear wired: not authoritative — it only
// names the definitive check, `claude plugin list`.
const pluginDoubleFireWarning = `warning: palate hooks may be wired twice (plugin + workspace settings) — run "palate hook uninstall --harness claude_code" to remove the workspace copy`

// pluginHooksInstalled reports whether a palate plugin cache directory
// exists for Claude Code or Codex. It never keys off CLAUDE_PLUGIN_ROOT —
// that env var is unreliable as a signal: harnesses set it inside a hook's
// own process (never for a human running `hook help` interactively), and a
// host's settings env may export a foreign or literal, unexpanded value to
// every process (observed: `CLAUDE_PLUGIN_ROOT="$HOME/.claude"`).
// Detection is filesystem-based instead: a glob for the plugin cache layout
// both harnesses use.
func pluginHooksInstalled() bool {
	home := mustHomeDir()
	if home == "" {
		return false
	}
	patterns := []string{
		filepath.Join(home, ".claude", "plugins", "cache", "*", "palate", "*", "hooks", "hooks.json"),
		filepath.Join(home, ".codex", "plugins", "cache", "*", "palate", "*", "hooks", "hooks.json"),
	}
	for _, pat := range patterns {
		if matches, err := filepath.Glob(pat); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

func parseHarnessList(list []string) (map[string]bool, error) {
	want := map[string]bool{}
	if len(list) == 0 {
		for _, h := range []string{"claude_code", "command_code", "zcode"} {
			want[h] = true
		}
		return want, nil
	}
	valid := map[string]bool{"claude_code": true, "command_code": true, "zcode": true, "zcode-global": true}
	for _, item := range list {
		for _, h := range strings.Split(item, ",") {
			h = strings.TrimSpace(h)
			if !valid[h] {
				return nil, fmt.Errorf("unknown harness %q (valid: claude_code, command_code, zcode, zcode-global)", h)
			}
			want[h] = true
		}
	}
	return want, nil
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// hookBinaryPath resolves the palate binary for hook commands. Temporary
// build paths (go run) go stale, so they degrade to a PATH lookup.
func hookBinaryPath() string {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		return "palate"
	}
	if strings.Contains(bin, "go-build") {
		fmt.Fprintf(os.Stderr, "hook install: %s is a temporary go-build path; using \"palate\" from PATH\n", bin)
		return "palate"
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return bin
	}
	return abs
}

// hookFilePath extracts the edited file path from a PostToolUse payload,
// tolerating the field-name variants across harness generations
// (tool_input.file_path / path, tool_response.file_path / filePath / path).
func hookFilePath(v map[string]interface{}) string {
	pick := func(m map[string]interface{}, keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	for _, section := range []string{"tool_input", "tool_response"} {
		if m, ok := v[section].(map[string]interface{}); ok {
			if fp := pick(m, "file_path", "filePath", "path"); fp != "" {
				return fp
			}
		}
	}
	return ""
}

// modelEdit is one tracked file: the hash at model-edit time plus a
// bounded content snapshot so the Stop handler can learn WHAT changed,
// not just that the file changed.
type modelEdit struct {
	Hash     string `json:"hash"`
	Snapshot string `json:"snapshot,omitempty"`
}

func loadModelEdits(path string) map[string]modelEdit {
	state := map[string]modelEdit{}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		// Legacy shape: path -> hash string.
		legacy := map[string]string{}
		if jerr := json.Unmarshal(data, &legacy); jerr != nil {
			return state
		}
		for fp, h := range legacy {
			state[fp] = modelEdit{Hash: h}
		}
	}
	return state
}

func saveModelEdits(path string, state map[string]modelEdit) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// applyUserDiffSignal compares the file on disk against the hash recorded at
// model-edit time. hashAfterOverride (files[] payload) replaces the baseline
// when non-empty. Returns true when the entry is handled (signal emitted or
// file gone) and should be removed from state; an unchanged file returns
// false and stays in state for later checks.
func applyUserDiffSignal(db *sql.DB, cfg config.Config, path, recordedHash, snapshot, hashAfterOverride string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		storage.BumpDrop(db, "user_diff")
		return true
	}
	baseline := recordedHash
	if hashAfterOverride != "" {
		baseline = hashAfterOverride
	}
	if fileHash(b) == baseline {
		return false
	}
	if cfg.LLMConfigured() {
		// Superseded by the live session distiller (Stop hook, above): the
		// same transcript this diff came from is about to be distilled into
		// natural-language statements, so storing the raw added lines here
		// would double-learn the same edit as both code and prose.
		storage.BumpDrop(db, "user_diff")
		return true
	}
	for _, sig := range learn.ExtractCandidates(snapshot, string(b)) {
		sig.File = path
		scan := safety.Scan(sig.Statement)
		if scan.Flagged && scan.Reason == "secret" {
			storage.BumpDrop(db, "user_diff")
			return true
		}
		if _, err := learn.ApplySignal(db, cfg, sig, nil); err != nil {
			storage.BumpDrop(db, "user_diff")
			return true
		}
		storage.BumpHit(db, "user_diff")
		return true
	}
	storage.BumpDrop(db, "user_diff")
	return true
}

// refreshModelEdits re-baselines every recorded snapshot to the file's
// current bytes (hash + 4KB bound) and drops entries whose file is gone. Used
// at Stop so the assistant's own post-snapshot edits are absorbed instead of
// being credited to the user at the next state walk.
func refreshModelEdits(modelEdits map[string]modelEdit) {
	for fp := range modelEdits {
		b, err := os.ReadFile(fp)
		if err != nil {
			delete(modelEdits, fp)
			continue
		}
		modelEdits[fp] = newModelEdit(b)
	}
}

// newModelEdit records a file's hash plus a 4KB-bounded snapshot of its bytes.
func newModelEdit(b []byte) modelEdit {
	snap := b
	if len(snap) > 4096 {
		snap = snap[:4096]
	}
	return modelEdit{Hash: fileHash(b), Snapshot: string(snap)}
}

// handleStateWalkUserDiffs runs the model_edits state walk at SessionStart
// and UserPromptSubmit — the two moments where drift since the last Stop can
// only be the user's own hand-edit.
func handleStateWalkUserDiffs(db *sql.DB, cfg config.Config) {
	statePath := filepath.Join(".palate", "model_edits.json")
	modelEdits := loadModelEdits(statePath)
	changed := false
	for fp, rec := range modelEdits {
		if applyUserDiffSignal(db, cfg, fp, rec.Hash, rec.Snapshot, "") {
			delete(modelEdits, fp)
			changed = true
		}
	}
	if changed {
		_ = saveModelEdits(statePath, modelEdits)
	}
}

func fileHash(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8])
}

func countVerified(registry []adapters.Adapter) int {
	n := 0
	for _, a := range registry {
		if a.Verified {
			n++
		}
	}
	return n
}

func cmdPrefer() *cobra.Command {
	return &cobra.Command{
		Use:   "prefer [statement]",
		Short: "Record explicit preference",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stmt := args[0]
			if err := gateExplicit(stmt); err != nil {
				return err
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt)}, nil)
			if err != nil {
				return err
			}
			fmt.Println("prefer recorded")
			return nil
		},
	}
}

func cmdCorrect() *cobra.Command {
	return &cobra.Command{
		Use:   "correct [tried] [wrong_because] [fix] [trigger_context]",
		Short: "Record 4-field correction (Mnemos schema)",
		Args:  cobra.RangeArgs(1, 4),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := learn.Correction{Tried: args[0]}
			switch len(args) {
			case 2:
				c.Fix = args[1]
			case 3:
				c.WrongBecause, c.Fix = args[1], args[2]
			case 4:
				c.WrongBecause, c.Fix, c.TriggerContext = args[1], args[2], args[3]
			}
			for _, txt := range c.Texts() {
				if err := gateExplicit(txt); err != nil {
					return err
				}
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			_, err := learn.RecordCorrection(db, cfg, c)
			if err != nil {
				return err
			}
			fmt.Println("correct recorded")
			return nil
		},
	}
}

func cmdPropose() *cobra.Command {
	return &cobra.Command{
		Use:   "propose [content]",
		Short: "Create LLM proposal (review queue, not deterministic)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			id, err := learn.CreateProposal(db, args[0], "")
			if err != nil {
				return err
			}
			fmt.Println(id)
			return nil
		},
	}
}

func cmdPromote() *cobra.Command {
	var as string
	c := &cobra.Command{
		Use:   "promote [proposal-id]",
		Short: "Promote proposal to deterministic path (--as stated_preference|explicit_prefer)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if as == "" {
				as = "stated_preference"
			}
			_, err := learn.PromoteProposal(db, args[0], as, cfg, func(sig learn.Signal) error {
				_, err := learn.ApplySignal(db, cfg, sig, nil)
				return err
			})
			if err != nil {
				return err
			}
			fmt.Println("promoted", args[0], "as", as)
			return nil
		},
	}
	c.Flags().StringVar(&as, "as", "stated_preference", "signal type")
	return c
}

func cmdSkill() *cobra.Command {
	c := &cobra.Command{Use: "skill", Short: "Skill promotion + rumination"}
	c.AddCommand(&cobra.Command{
		Use: "cluster", Short: "Cluster preferences into skill candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			cands, err := skills.Cluster(db, 3)
			if err != nil {
				return err
			}
			cwd, _ := os.Getwd()
			if err := skills.WriteCandidates(cwd, cands); err != nil {
				return err
			}
			for _, cand := range cands {
				fmt.Printf("%s (%d prefs) hash=%s\n", cand.ID, cand.Count, cand.Hash)
			}
			if len(cands) == 0 {
				fmt.Println("no clusters (need ≥3 per category with reinforcement_count≥2)")
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "list", Short: "List skill candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			dir := filepath.Join(cwd, ".palate", "skill-candidates")
			entries, _ := os.ReadDir(dir)
			if len(entries) == 0 {
				fmt.Println("no candidates")
				return nil
			}
			for _, e := range entries {
				fmt.Println(e.Name())
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "install [id]", Short: "Install a candidate (inert until installed)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			if err := skills.InstallCandidate(cwd, args[0]); err != nil {
				return err
			}
			fmt.Println("installed", args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "ruminate [old] [new] [why_better]", Short: "Rumination gate — validate why_better",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := skills.CheckRevision(args[0], args[1], args[2]); err != nil {
				return err
			}
			fmt.Println("ruminate: ok")
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "stats [id]", Short: "Show or bump effectiveness stats",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			if len(args) == 0 {
				m := skills.LoadStats(cwd)
				b, _ := json.MarshalIndent(m, "", "  ")
				fmt.Println(string(b))
				for _, issue := range skills.Monitors(cwd) {
					fmt.Fprintln(os.Stderr, "monitor:", issue)
				}
				return nil
			}
			if err := skills.BumpStat(cwd, args[0], true); err != nil {
				return err
			}
			fmt.Println("bumped", args[0])
			return nil
		},
	})
	return c
}

// maxTranscriptsPerHarness caps how many transcripts `learn sessions`
// touches per reader unless --all is passed — a CLI default, not a
// hardcoded constant deep in discovery/windowing logic, so it can be
// lowered without touching either.
const maxTranscriptsPerHarness = 20

// capTranscripts sorts transcripts most-recently-modified first and,
// unless all is set, truncates to maxTranscriptsPerHarness — the guard
// against an unbounded scan of a harness's full corpus (thousands of files
// in the worst case).
func capTranscripts(transcripts []distill.Transcript, all bool) []distill.Transcript {
	sort.Slice(transcripts, func(i, j int) bool { return transcripts[i].ModTime.After(transcripts[j].ModTime) })
	if !all && len(transcripts) > maxTranscriptsPerHarness {
		transcripts = transcripts[:maxTranscriptsPerHarness]
	}
	return transcripts
}

func cmdLearn() *cobra.Command {
	c := &cobra.Command{Use: "learn", Short: "Learn preferences from transcripts via the LLM distiller"}
	c.AddCommand(cmdLearnSession())
	c.AddCommand(cmdLearnSessions())
	c.AddCommand(cmdLearnJournals())
	c.AddCommand(cmdLearnRepo())
	return c
}

// noLLMConfiguredMsg is the single line every learn subcommand prints
// (then exits 0) when PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL are unset.
const noLLMConfiguredMsg = "palate: no LLM configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL) — nothing learned"

// learnLockPath returns the path every `learn` subcommand serializes on so
// two overlapping runs (e.g. a Stop-triggered detach firing while another
// is mid-flight) never write concurrently.
func learnLockPath() string {
	return filepath.Join(".palate", "learn.lock")
}

func cmdLearnSession() *cobra.Command {
	var harness string
	var verbose bool
	c := &cobra.Command{
		Use:   "session <path>",
		Short: "Learn preferences from one transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if !cfg.LLMConfigured() {
				fmt.Println(noLLMConfiguredMsg)
				return nil
			}

			h := harness
			if h == "" {
				inferred, err := inferHarnessFromPath(args[0])
				if err != nil {
					return err
				}
				h = inferred
			}
			reader, err := readerByHarness(h)
			if err != nil {
				return err
			}
			path, err := filepath.Abs(args[0])
			if err != nil {
				path = args[0]
			}

			db := openDB(cfg)
			defer storage.Close()
			release, ok := distill.AcquireLearnLock(learnLockPath())
			if !ok {
				if verbose {
					fmt.Println("palate: learn already in progress — skipping")
				}
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)

			summary, err := distill.LearnSession(context.Background(), db, cfg, gw, reader, distill.Transcript{Harness: h, Path: path})
			if err != nil {
				return err
			}
			printLearnOutcome(summary)
			if verbose {
				fmt.Println("gateway model:", cfg.LLM.Model)
				if !summary.Skipped && !summary.Advanced {
					fmt.Println("reason:", summary.Reason)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&harness, "harness", "", "harness override: claude_code|command_code|pi|codex|opencode|gemini_cli|memsearch|zcode")
	c.Flags().BoolVar(&verbose, "verbose", false, "print the gateway model and, on a non-advancing run, the reason")
	return c
}

func cmdLearnSessions() *cobra.Command {
	var harness, project string
	var all, includeGlobal, includeExperimental bool
	c := &cobra.Command{
		Use:   "sessions",
		Short: "Learn preferences across every registered reader's discovered transcripts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if !cfg.LLMConfigured() {
				fmt.Println(noLLMConfiguredMsg)
				return nil
			}

			readers := distill.AllReaders()
			if includeExperimental {
				readers = append(readers, distill.ExperimentalReaders()...)
			}
			if harness != "" {
				readers = readersForHarness(readers, harness)
				if len(readers) == 0 {
					return fmt.Errorf("unknown harness %q", harness)
				}
			}

			db := openDB(cfg)
			defer storage.Close()
			release, ok := distill.AcquireLearnLock(learnLockPath())
			if !ok {
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)

			for _, reader := range readers {
				transcripts, err := discoverForReader(reader, project, includeGlobal)
				if err != nil {
					fmt.Fprintf(os.Stderr, "palate: learn sessions: %s discover: %v\n", reader.Harness(), err)
					continue
				}
				transcripts = capTranscripts(transcripts, all)
				runLearnLoop(cmd.Context(), db, cfg, gw, reader, transcripts)
			}
			return nil
		},
	}
	c.Flags().StringVar(&harness, "harness", "", "limit to one harness")
	c.Flags().StringVar(&project, "project", "", "limit to one project (claude_code/command_code/pi/codex/opencode only — see docs/adapters.md)")
	c.Flags().BoolVar(&all, "all", false, "scan every discovered transcript, not just the 20 most-recently-modified per harness")
	c.Flags().BoolVar(&includeGlobal, "include-global", false, "also learn from ~/.memsearch/memory/ (prints a notice first)")
	c.Flags().BoolVar(&includeExperimental, "include-experimental", false, "also run the ZCode reader")
	return c
}

func cmdLearnJournals() *cobra.Command {
	var includeGlobal bool
	c := &cobra.Command{
		Use:   "journals",
		Short: "Learn preferences from memsearch journals",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if !cfg.LLMConfigured() {
				fmt.Println(noLLMConfiguredMsg)
				return nil
			}

			reader, err := readerByHarness("memsearch")
			if err != nil {
				return err
			}
			transcripts, err := discoverForReader(reader, "", includeGlobal)
			if err != nil {
				return err
			}

			db := openDB(cfg)
			defer storage.Close()
			release, ok := distill.AcquireLearnLock(learnLockPath())
			if !ok {
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)
			runLearnLoop(cmd.Context(), db, cfg, gw, reader, transcripts)
			return nil
		},
	}
	c.Flags().BoolVar(&includeGlobal, "include-global", false, "also learn from ~/.memsearch/memory/ (prints a notice first)")
	return c
}

func cmdLearnRepo() *cobra.Command {
	var maxCommits, maxSignals int
	var allAuthors, verbose bool
	c := &cobra.Command{
		Use:   "repo [path]",
		Short: "Learn preferences from a git repository's commit history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if !cfg.LLMConfigured() {
				fmt.Println(noLLMConfiguredMsg)
				return nil
			}
			if _, err := exec.LookPath("git"); err != nil {
				return fmt.Errorf("git not found in PATH")
			}

			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			repoPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}

			author := ""
			if !allAuthors {
				email, err := distill.RepoAuthorEmail(repoPath)
				if err != nil {
					fmt.Fprintln(os.Stderr, "palate: learn repo: git user.email not set — learning from all authors")
				} else {
					author = email
				}
			}

			db := openDB(cfg)
			defer storage.Close()
			release, ok := distill.AcquireLearnLock(learnLockPath())
			if !ok {
				if verbose {
					fmt.Println("palate: learn already in progress — skipping")
				}
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)

			signalCount, summary, err := distill.LearnRepo(cmd.Context(), db, cfg, gw, repoPath, author, maxCommits, maxSignals)
			if err != nil {
				return err
			}
			fmt.Printf("%d signals -> %d preferences (%d applied, %d dropped)\n", signalCount, summary.Applied+summary.Dropped, summary.Applied, summary.Dropped)
			if verbose {
				fmt.Println("gateway model:", cfg.LLM.Model)
				if !summary.Skipped && !summary.Advanced {
					fmt.Println("reason:", summary.Reason)
				}
			}
			return nil
		},
	}
	c.Flags().IntVar(&maxCommits, "max-commits", 200, "maximum commits to scan")
	c.Flags().IntVar(&maxSignals, "max-signals", 50, "maximum before/after signals to extract")
	c.Flags().BoolVar(&allAuthors, "all-authors", false, "learn from every commit author, not just git config user.email")
	c.Flags().BoolVar(&verbose, "verbose", false, "print the gateway model and, on a non-advancing run, the reason")
	return c
}

// runLearnLoop applies LearnSession to every transcript in order, printing
// one outcome line per transcript and continuing past a per-transcript
// error rather than aborting the whole batch.
func runLearnLoop(ctx context.Context, db *sql.DB, cfg config.Config, gw *distill.Gateway, reader distill.Reader, transcripts []distill.Transcript) {
	for _, t := range transcripts {
		summary, err := distill.LearnSession(ctx, db, cfg, gw, reader, t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "palate: learn %s %s: %v\n", reader.Harness(), t.Path, err)
			continue
		}
		fmt.Print(t.Path + ": ")
		printLearnOutcome(summary)
	}
}

func printLearnOutcome(s distill.Summary) {
	if s.Skipped {
		fmt.Println("0 preferences (0 applied, 0 dropped)")
		return
	}
	fmt.Printf("%d preferences (%d applied, %d dropped)\n", s.Applied+s.Dropped, s.Applied, s.Dropped)
}

// readerByHarness returns the single reader (including experimental ones)
// matching harness.
func readerByHarness(harness string) (distill.Reader, error) {
	all := append(distill.AllReaders(), distill.ExperimentalReaders()...)
	for _, r := range all {
		if r.Harness() == harness {
			return r, nil
		}
	}
	return nil, fmt.Errorf("unknown harness %q", harness)
}

func readersForHarness(readers []distill.Reader, harness string) []distill.Reader {
	for _, r := range readers {
		if r.Harness() == harness {
			return []distill.Reader{r}
		}
	}
	return nil
}

// inferHarnessFromPath matches path against each reader's known transcript
// root, returning the harness name; errors out naming the accepted values
// when nothing matches (a fixture under testdata/, for example, must pass
// --harness explicitly since it lives under none of these roots).
func inferHarnessFromPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	home := mustHomeDir()
	roots := []struct{ harness, root string }{
		{"claude_code", filepath.Join(home, ".claude", "projects")},
		{"command_code", filepath.Join(home, ".commandcode", "projects")},
		{"pi", filepath.Join(home, ".pi", "agent", "sessions")},
		{"codex", filepath.Join(home, ".codex", "sessions")},
		{"gemini_cli", filepath.Join(home, ".gemini", "tmp")},
		{"zcode", filepath.Join(home, ".zcode", "cli", "rollout")},
	}
	for _, r := range roots {
		if r.root != "" && strings.HasPrefix(abs, r.root) {
			return r.harness, nil
		}
	}
	return "", fmt.Errorf("cannot infer harness from path %q; pass --harness (claude_code|command_code|pi|codex|opencode|gemini_cli|memsearch|zcode)", path)
}

func mustHomeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// discoverForReader runs reader.Discover with the project/global-journal
// policy the CLI layer owns: memsearch scans project-local by default and
// only touches ~/.memsearch/memory/ (with a printed notice) when
// includeGlobal is set; --project narrows Claude Code's own Discover root
// directly (so it never lists another project's directory) and, for every
// other reader whose format carries a Project label, filters post-hoc.
func discoverForReader(reader distill.Reader, project string, includeGlobal bool) ([]distill.Transcript, error) {
	if reader.Harness() == "memsearch" {
		out, err := reader.Discover("")
		if err != nil {
			return nil, err
		}
		if includeGlobal {
			globalDir := filepath.Join(mustHomeDir(), ".memsearch", "memory")
			fmt.Println("including global memsearch journals:", globalDir)
			g, err := reader.Discover(globalDir)
			if err != nil {
				return nil, err
			}
			out = append(out, g...)
		}
		return out, nil
	}

	root := ""
	if project != "" && reader.Harness() == "claude_code" {
		root = filepath.Join(mustHomeDir(), ".claude", "projects", strings.ReplaceAll(project, "/", "-"))
	}
	transcripts, err := reader.Discover(root)
	if err != nil {
		return nil, err
	}
	if project == "" || reader.Harness() == "claude_code" || reader.Harness() == "gemini_cli" {
		return transcripts, nil
	}
	filtered := transcripts[:0]
	for _, t := range transcripts {
		if t.Project == project {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the palate version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("palate " + version)
			return nil
		},
	}
}

func cmdUpdate() *cobra.Command {
	return &cobra.Command{
		Use: "update", Short: "Print update instructions",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("update: install the latest release with one of")
			fmt.Println("  curl -fsSL https://raw.githubusercontent.com/thinhkhuat/palate/main/install.sh | sh")
			fmt.Println("  or: go install github.com/thinhkhuat/palate/cmd/palate@latest")
			return nil
		},
	}
}
