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
	"github.com/thinhkhuat/palate/internal/consolidate"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/judge"
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

// spawnConsolidate is the same seam for the auto-fired consolidation pass.
var spawnConsolidate = distill.SpawnConsolidate

func loadConfig() config.Config {
	if cfgFile == "" {
		cfgFile = "config.yaml"
	}
	cfg, _ := config.Load(cfgFile)
	render.RegisterAuto(cfg)
	return cfg
}

// modeName is the one-word mode shown by init and doctor.
func modeName(cfg config.Config) string {
	if cfg.Legacy() {
		return "legacy-repo"
	}
	return "global"
}

func openDB(cfg config.Config) *sql.DB {
	_ = cfg.EnsureLayout()
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
	root.AddCommand(cmdRestore())
	root.AddCommand(cmdExport())
	root.AddCommand(cmdLint())
	root.AddCommand(cmdReimport())
	root.AddCommand(cmdPush())
	root.AddCommand(cmdPull())
	root.AddCommand(cmdTaste())
	root.AddCommand(cmdMigrate())
	root.AddCommand(cmdConsolidate())
	root.AddCommand(cmdDream())
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
	wireDistillJudge()
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// wireDistillJudge routes every distilled statement through the judge before
// it is stored (same / refines / contradicts / new), like every explicit
// write. The seam lives in distill because judge imports it; main() calls
// this once, and the test that proves the wiring calls it too.
func wireDistillJudge() { distill.Judge = judge.ApplyDistilled }

func cmdInit() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize the Palate home (DB, state/, backups/, config.yaml)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if err := cfg.EnsureLayout(); err != nil {
				return err
			}
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			// Seed the notice cursor now, so everything recorded after init —
			// including preferences stated before the first hook ever runs —
			// is announced instead of swallowed by the first hook's seeding.
			seedNoticeCursor(db, cfg)
			storage.Close()
			fmt.Printf("palate initialized: home %s (%s)\n", cfg.Home, modeName(cfg))
			fmt.Println("db:", cfg.DBPath)
			return nil
		},
	}
}

// staleGatewayWarning compares the live PALATE_LLM_* env with the model the
// last successful gateway call actually used (state/last_gateway.json). A
// mismatch means running hooks/servers hold an older env than a fresh shell
// would get — doctor's only honest move is to say so; it cannot know what a
// future shell will export.
func staleGatewayWarning(cfg config.Config) string {
	if !cfg.LLMConfigured() {
		return ""
	}
	model, at, ok := distill.LastGatewayStamp(cfg)
	if !ok || model == cfg.LLM.Model {
		return ""
	}
	return fmt.Sprintf("model changed since the last gateway call (env %s, last used %s at %s) — running hooks/servers still carry the old env; restart the harness to re-source it", cfg.LLM.Model, model, at)
}

func cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Self-diagnostic",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			fmt.Printf("config: %s\n", cfgFile)
			fmt.Printf("home: %s  mode: %s\n", cfg.Home, modeName(cfg))
			if cfg.Legacy() {
				fmt.Println("home: legacy per-repo layout — move it into the global home losslessly with: palate migrate")
			}
			fmt.Printf("db: %s\n", cfg.DBPath)
			fmt.Printf("taste: %s (auto-render %s)\n", cfg.TastePath(), map[bool]string{true: "on", false: "off"}[cfg.Render.Auto])
			fmt.Printf("judge: %s\n", judge.Mode(cfg))
			if warn := staleGatewayWarning(cfg); warn != "" {
				fmt.Printf("gateway: %s\n", warn)
			}
			fmt.Printf("learning: %s\n", cfg.LearningMode())
			if bak := storage.NewestBackup(cfg.BackupDir()); bak != "" {
				fmt.Printf("backup: newest %s\n", bak)
			} else {
				fmt.Printf("backup: none yet (written before forget/pull into %s)\n", cfg.BackupDir())
			}
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
			since, every, due := consolidate.Due(db, cfg)
			fmt.Printf("consolidate: %d events since last pass / every %d (auto-fire %s)\n", since, every, map[bool]string{true: "due", false: "not due"}[due])
			var v string
			if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err == nil {
				fmt.Printf("sqlite: %s\n", v)
			}
			var fts int
			_ = db.QueryRow(`SELECT count(*) FROM fts_working WHERE fts_working MATCH 'test'`).Scan(&fts)
			fmt.Printf("fts5: ok\n")
			fmt.Printf("vector: json+cosine (native vec behind build tag)\n")
			// Two different claims, printed separately: Verified is the
			// build-time docs check ("CAN learn once wired"), LiveWiring is
			// what is actually installed on THIS machine right now.
			live := adapters.LiveWiring()
			var wired []string
			for _, a := range adapters.Registry {
				if w, ok := live[a.Name]; ok && w.State == adapters.WiringWired {
					wired = append(wired, a.Name)
				}
			}
			wiredNames := "none"
			if len(wired) > 0 {
				wiredNames = strings.Join(wired, ", ")
			}
			fmt.Printf("adapters: %d registry (%d verified for learning; live-wired: %s)\n", len(adapters.Registry), countVerified(adapters.Registry), wiredNames)
			for _, a := range adapters.Registry {
				status := "recall-only"
				if a.Verified {
					status = "learning"
				}
				w, _ := live[a.Name]
				wire := "not wired"
				detail := ""
				switch w.State {
				case adapters.WiringWired:
					wire = "wired"
				case adapters.WiringAbsent:
					wire = "harness absent"
				}
				if w.State == adapters.WiringWired && w.Detail != "" {
					detail = " (" + w.Detail + ")"
				}
				fmt.Printf("adapter: %-16s %-12s %-16s%s — %s\n", a.Name, status, wire, detail, a.Install)
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
			markRecallProfile(db, cfg, results)
			if jsonOut {
				b, _ := json.MarshalIndent(results, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			for _, r := range results {
				marker := ""
				if r.Table == "preferences" && r.InProfile {
					marker = " [profile]"
				}
				// The marker sits before "id=" so extractID-style parsing
				// (find "id=", read to end of line) never picks it up.
				fmt.Printf("[%s] %s (%.3f)%s id=%s\n", r.Table, r.Content, r.Score, marker, r.ID)
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

// markRecallProfile sets InProfile on every preferences result, computed
// with the same learn.SelectProfile the SessionStart block uses (phase-02
// req 14) — recall keeps searching the whole live ledger; this only marks
// which of the hits also made the budgeted profile.
func markRecallProfile(db *sql.DB, cfg config.Config, results []storage.RecallResult) {
	needsMark := false
	for _, r := range results {
		if r.Table == "preferences" {
			needsMark = true
			break
		}
	}
	if !needsMark {
		return
	}
	profile, err := learn.SelectProfile(db, cfg, render.ProfileBudget(cfg))
	if err != nil {
		return
	}
	inProfile := make(map[string]bool, len(profile.In))
	for _, p := range profile.In {
		inProfile[p.ID] = true
	}
	for i := range results {
		if results[i].Table == "preferences" {
			results[i].InProfile = inProfile[results[i].ID]
		}
	}
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
			if err == nil && cfg.LLMConfigured() {
				// Same rules as the verb and the auto-fire: only when the
				// watermark says a pass is due, and never concurrently with
				// another learn/consolidate process.
				if _, _, due := consolidate.Due(db, cfg); due {
					if release, locked := distill.AcquireLearnLock(learnLockPath(cfg)); locked {
						if rep, cerr := runConsolidatePass(cmd.Context(), cfg, db, "sleep", consolidate.RunOptions{}, nil); cerr != nil {
							fmt.Fprintln(os.Stderr, "sleep: consolidation skipped:", cerr)
						} else {
							fmt.Printf("consolidated: %s mode=%s\n", rep.Summary(), rep.Mode)
						}
						release()
					} else {
						recordSkippedLock(cfg, db, "sleep")
						fmt.Fprintln(os.Stderr, "sleep: consolidation skipped — another learn/consolidate pass holds the lock")
					}
				}
			}
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
		// A transport error is a runtime event, not a usage mistake: one line, no flag dump.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			// Clean up launchers that earlier sessions left stuck before exec.
			go reapLauncherGhosts(cfg.StatePath("launcher-ghosts.log"), ghostMinAge)
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
			// capture counters: one line per signal, so judge_llm / judge_fallback
			// and the consolidation counters are visible without sqlite
			if rows, err := db.Query(`SELECT signal_type, hits, drops FROM capture_counters ORDER BY signal_type`); err == nil {
				defer rows.Close()
				for rows.Next() {
					var name string
					var hits, drops int
					if rows.Scan(&name, &hits, &drops) == nil {
						fmt.Printf("counter: %s hits=%d drops=%d\n", name, hits, drops)
					}
				}
			}
			return nil
		},
	}
}

func cmdForget() *cobra.Command {
	var purge bool
	c := &cobra.Command{
		Use:   "forget [id]",
		Short: "Forget by id (soft for a preference: restorable with `palate restore`; --purge deletes it for good)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			label := "forget"
			if purge {
				label = "purge"
			}
			if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), label); err != nil {
				return fmt.Errorf("backup before %s: %w", label, err)
			} else if bak != "" {
				fmt.Fprintln(os.Stderr, "backup:", bak)
			}
			if purge {
				if err := storage.Purge(db, args[0]); err != nil {
					return err
				}
				fmt.Println("purged", args[0])
				return nil
			}
			if err := storage.Forget(db, args[0]); err != nil {
				return err
			}
			fmt.Println("forgot", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&purge, "purge", false, "hard-delete a preference instead of the default soft (restorable) forget")
	return c
}

// cmdRestore reverses a soft forget/retire, or promotes a live candidate that
// fell out of the profile back to STATED (phase-02 req 11).
func cmdRestore() *cobra.Command {
	return &cobra.Command{
		Use:   "restore [id]",
		Short: "Restore a retired preference, or promote a candidate that fell out of the profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "restore"); err != nil {
				return fmt.Errorf("backup before restore: %w", err)
			} else if bak != "" {
				fmt.Fprintln(os.Stderr, "backup:", bak)
			}
			if _, err := learn.Restore(db, args[0]); err != nil {
				return err
			}
			fmt.Println("restored", args[0])
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
		Use:   "lint [path]",
		Short: "Lint TASTE.md (default: the auto-maintained file in the Palate home)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := loadConfig().TastePath()
			if len(args) > 0 {
				path = args[0]
			}
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "lint: no TASTE.md at %s\n", path)
				fmt.Fprintln(os.Stderr, "lint: it is written automatically after the first learned preference; for a git-shareable copy run: palate push")
				fmt.Fprintln(os.Stderr, "lint: note: TASTE.md is a render; the events log in palate.db is the source of truth")
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
			n, err := render.Reimport(db, cfg, path)
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
			if err := render.WriteFile(db, cfg, path); err != nil {
				return err
			}
			fmt.Printf("TASTE.md rendered from DB (lossy artifact, events is truth). Next: git add %s && git commit -m 'palate: update TASTE.md' && git push\n", path)
			return nil
		},
	}
}

// cmdTaste re-syncs the auto-maintained TASTE.md from the DB (a DB restored
// or edited outside Palate leaves the render stale — nothing marked it
// dirty) and prints it. The slash command /palate:taste calls this.
//
// --cut and --path leave a pending hold alone and print one warning line.
// --accept-edits and plain `taste` on a pending hold resolve it first
// (accepting stores the held additions/deletions, plain discards them) and
// remove the stale on-disk file so the Sync right after starts clean instead
// of re-detecting the very edit that was just resolved.
func cmdTaste() *cobra.Command {
	var pathOnly, cut, acceptEdits, yes bool
	c := &cobra.Command{
		Use:          "taste",
		Short:        "Re-sync and print the auto-maintained TASTE.md",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes && !acceptEdits {
				return fmt.Errorf("--yes only goes with --accept-edits")
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if acceptEdits {
				if err := render.AcceptHeldEdits(db, cfg, yes); err != nil {
					return err
				}
			} else if render.HoldPending(cfg) {
				if cut || pathOnly {
					// A read-only view never resolves a hold.
					msg := "palate: a hand edit to TASTE.md is held and was left alone; run `palate taste --accept-edits` to apply it or plain `palate taste` to discard it"
					if !render.HoldAcceptable(cfg) {
						msg = "palate: a hand edit to TASTE.md is held and was left alone; it cannot be applied safely, run plain `palate taste` to discard it"
					}
					fmt.Fprintln(os.Stderr, msg)
				} else if err := render.DiscardHold(cfg); err != nil {
					return err
				}
			} else if err := render.ReadBack(db, cfg, cfg.TastePath()); err != nil {
				return err
			}
			if _, err := render.Sync(db, cfg, cfg.TastePath()); err != nil {
				return err
			}
			if cut {
				return printProfileCut(db, cfg)
			}
			if pathOnly {
				fmt.Println(cfg.TastePath())
				return nil
			}
			b, err := os.ReadFile(cfg.TastePath())
			if err != nil {
				return err
			}
			fmt.Print(string(b))
			return nil
		},
	}
	c.Flags().BoolVar(&pathOnly, "path", false, "print only the file path")
	c.Flags().BoolVar(&cut, "cut", false, "list every live row excluded from the profile, with its id and reason")
	c.Flags().BoolVar(&acceptEdits, "accept-edits", false, "apply a held hand-edit (large addition, mass deletion or agent edit) that read-back could not apply safely on its own; a hold with no verifiable base can only be discarded")
	c.Flags().BoolVar(&yes, "yes", false, "with --accept-edits: also bring back lines that have left the profile, and accept more than 10 added lines, for any held edit (every such line is listed; without --yes nothing is applied)")
	return c
}

// printProfileCut implements `palate taste --cut`: id + reason for every live
// row SelectProfile left out, ranked the same way SelectProfile itself would
// (STATED first, sessions desc, confidence desc, id asc) — an approximation
// of its exact tie-break chain built from the same exported reads, since the
// full ranked candidate list is private to learn.SelectProfile.
func printProfileCut(db *sql.DB, cfg config.Config) error {
	profile, err := learn.SelectProfile(db, cfg, render.ProfileBudget(cfg))
	if err != nil {
		return err
	}
	if len(profile.Excluded) == 0 {
		fmt.Println("taste --cut: every live row is in the profile")
		return nil
	}
	prefs, err := learn.ListPreferences(db, true)
	if err != nil {
		return err
	}
	sessions, err := learn.SessionCounts(db)
	if err != nil {
		return err
	}
	type cutRow struct {
		id, reason string
		stated     bool
		sessions   int
		confidence float64
	}
	var rows []cutRow
	for _, p := range prefs {
		reason, ok := profile.Excluded[p.ID]
		if !ok {
			continue
		}
		rows = append(rows, cutRow{id: p.ID, reason: reason, stated: p.TrustTier == "STATED", sessions: sessions[p.ID], confidence: p.Confidence})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.stated != b.stated {
			return a.stated
		}
		if a.sessions != b.sessions {
			return a.sessions > b.sessions
		}
		if a.confidence != b.confidence {
			return a.confidence > b.confidence
		}
		return a.id < b.id
	})
	for _, r := range rows {
		fmt.Printf("%s\t%s\n", r.id, r.reason)
	}
	return nil
}

// cmdMigrate moves a legacy per-repo store (./.palate/) into the global home
// with its events, tiers and confidences intact, then parks the old directory
// under <global>/backups/. A TASTE.md round-trip would flatten every row to
// IMPORTED 0.4, so that path is only suggested when the global store already
// holds data of its own (a lossless merge of two SQLite stores is out of
// scope).
func cmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:          "migrate",
		Short:        "Move this repo's legacy .palate/ store into the global home (lossless)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := loadConfig()
			if !legacy.Legacy() {
				return fmt.Errorf("migrate: no legacy ./.palate/palate.db here (mode: %s)", modeName(legacy))
			}
			global := config.ForGlobalHome(legacy)
			if n := eventCount(global.DBPath); n > 0 {
				return fmt.Errorf("migrate: the global store %s already holds %d events; merge by reimport instead: palate push && (cd ~ && palate pull %s)", global.DBPath, n, filepath.Join(filepath.Dir(legacy.Home), "TASTE.md"))
			}
			if err := global.EnsureLayout(); err != nil {
				return err
			}
			if bak, err := storage.BackupBefore(global.DBPath, global.BackupDir(), "migrate"); err != nil {
				return err
			} else if bak != "" {
				fmt.Fprintln(os.Stderr, "backup:", bak)
			}
			storage.Close()
			if err := copyFile(legacy.DBPath, global.DBPath, 0600); err != nil {
				return fmt.Errorf("copy store: %w", err)
			}
			for _, sfx := range []string{"-wal", "-shm"} {
				_ = os.Remove(global.DBPath + sfx)
			}
			parked := filepath.Join(global.BackupDir(), fmt.Sprintf("legacy-%s-%s", filepath.Base(filepath.Dir(legacy.Home)), time.Now().Format("20060102-1504")))
			if err := os.Rename(legacy.Home, parked); err != nil {
				return fmt.Errorf("park legacy dir: %w", err)
			}
			render.RegisterAuto(global)
			db, err := storage.Open(global.DBPath)
			if err != nil {
				return err
			}
			defer storage.Close()
			if _, err := render.Sync(db, global, global.TastePath()); err != nil {
				return err
			}
			fmt.Printf("migrated %s -> %s (events, tiers and confidences intact)\n", legacy.DBPath, global.DBPath)
			fmt.Printf("legacy dir parked at %s\n", parked)
			fmt.Printf("taste: %s\n", global.TastePath())
			return nil
		},
	}
}

// eventCount returns the number of rows in events for an existing DB, 0 when
// the file is absent or unreadable — used to tell "fresh global store" from
// "already in use" without opening the process-wide singleton.
func eventCount(dbPath string) int {
	if _, err := os.Stat(dbPath); err != nil {
		return 0
	}
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
		return 0
	}
	return n
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// cmdConsolidate runs one whole-profile review pass through the gateway,
// plus a no-gateway verbatim cleanup (`--retire-verbatim`) for the retired
// file-diff path's "prefers: <code>" dumps: matched live verbatim rows retire
// through the events funnel after a backup, bypassing the 40% review guard
// (a poisoned store can be mostly garbage — that is exactly when the guard
// would block the cleanup). --dry-run counts without writing.
func cmdConsolidate() *cobra.Command {
	c := &cobra.Command{
		Use:          "consolidate",
		Short:        "Review the whole profile with the LLM gateway: merge, rewrite, rescore, retire (review mode → proposals)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			retireVerbatim, _ := cmd.Flags().GetBool("retire-verbatim")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			rebuild, _ := cmd.Flags().GetBool("rebuild")
			if retireVerbatim {
				cfg := loadConfig()
				db := openDB(cfg)
				defer storage.Close()
				ids, err := consolidate.RetireVerbatimRows(db, true)
				if err != nil {
					return err
				}
				if dryRun || len(ids) == 0 {
					fmt.Printf("verbatim scan: %d live verbatim rows (no changes)\n", len(ids))
					return nil
				}
				if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "consolidate-verbatim"); err != nil {
					return fmt.Errorf("backup before verbatim retire: %w", err)
				} else if bak != "" {
					fmt.Fprintln(os.Stderr, "backup:", bak)
				}
				retired, err := consolidate.RetireVerbatimRows(db, false)
				if err != nil {
					return err
				}
				fmt.Printf("retired %d verbatim rows (backup above, audit in events as consolidate_retire)\n", len(retired))
				return nil
			}
			cfg := loadConfig()
			if !cfg.LLMConfigured() {
				return fmt.Errorf("consolidate: no LLM gateway configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL)")
			}
			db := openDB(cfg)
			defer storage.Close()
			release, locked := distill.AcquireLearnLock(learnLockPath(cfg))
			if !locked {
				recordSkippedLock(cfg, db, "manual")
				fmt.Fprintln(os.Stderr, "consolidate: another learn/consolidate pass holds the lock; skipping")
				return nil
			}
			defer release()
			rep, err := runConsolidatePass(cmd.Context(), cfg, db, "manual", consolidate.RunOptions{Rebuild: rebuild}, nil)
			if err != nil {
				return err
			}
			fmt.Printf("consolidated %d rows: %s mode=%s watermark=event #%d\n",
				rep.Rows, rep.Summary(), rep.Mode, rep.Watermark)
			return nil
		},
	}
	c.Flags().Bool("retire-verbatim", false, "retire live verbatim work-product rows without a gateway (backup first)")
	c.Flags().Bool("dry-run", false, "with --retire-verbatim: count matches without retiring")
	c.Flags().Bool("rebuild", false, "review every live row (not just the delta), with no 40% guard — one supervised invocation")
	return c
}

// cmdDream is a thin alias for `palate consolidate` (the owner's word for the
// dream pass, red-team Q2): same flags, same runConsolidatePass underneath.
func cmdDream() *cobra.Command {
	c := cmdConsolidate()
	c.Use = "dream"
	c.Short = "Alias for `palate consolidate` — the dream pass"
	return c
}

// runConsolidatePass is the one shared runner behind `palate consolidate`,
// its `palate dream` alias, `palate sleep`, and `learn session
// --then-consolidate` (req 11): it builds the consolidate gateway (its own,
// longer timeout — req 10), runs one pass, prints the outcome and records the
// attempt (req 8). The caller must already hold learn.lock; this never
// acquires or releases it. learnErr, when non-nil, is a prior LearnSession
// failure from the same invocation (req 10a) — folded into the recorded
// attempt's Error alongside any error this pass itself hits.
func runConsolidatePass(ctx context.Context, cfg config.Config, db *sql.DB, trigger string, opts consolidate.RunOptions, learnErr error) (consolidate.Report, error) {
	start := time.Now()
	opts.Trigger = trigger
	opts.OnBatch = func() { _ = distill.TouchLock(learnLockPath(cfg)) }

	gw, ok := consolidate.BuildGateway(cfg)
	if !ok {
		err := fmt.Errorf("consolidate: no LLM gateway configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL)")
		recordAttempt(cfg, db, trigger, consolidate.Report{Outcome: "error"}, combineErrors(learnErr, err), start)
		return consolidate.Report{}, err
	}
	rep, err := consolidate.Run(ctx, db, cfg, gw, opts)
	recordAttempt(cfg, db, trigger, rep, combineErrors(learnErr, err), start)
	return rep, err
}

// recordAttempt writes one consolidate.log line for a completed
// runConsolidatePass call (req 8). Best-effort: a logging failure must never
// mask the pass's own result.
func recordAttempt(cfg config.Config, db *sql.DB, trigger string, rep consolidate.Report, combinedErr error, start time.Time) {
	outcome := rep.Outcome
	if outcome == "" {
		outcome = "error"
	}
	errStr := ""
	if combinedErr != nil {
		errStr = combinedErr.Error()
	}
	withNoticeLock(cfg, func() {
		_ = consolidate.RecordAttempt(cfg, consolidate.Attempt{
			Trigger:     trigger,
			Outcome:     outcome,
			Batches:     rep.Batches,
			Actions:     rep.Actions,
			Applied:     rep.Applied,
			Proposed:    rep.Proposed,
			Skipped:     rep.Skipped,
			SkipReasons: rep.SkipReasons,
			BatchMS:     rep.BatchMS,
			ApplyErrors: rep.ApplyErrors,
			Retired:     rep.Retired,
			Error:       errStr,
			DurationMS:  time.Since(start).Milliseconds(),
			EventID:     attemptEventID(db, outcome),
		})
	})
}

// attemptEventID is the event_id stamped on an attempt record (req 8). A
// failed or skipped attempt first appends a consolidate_attempt event, so its
// id lies strictly past the notice cursor of any notice before it and the next
// notice reports it exactly once; other outcomes just take MAX(events.id).
func attemptEventID(db *sql.DB, outcome string) int64 {
	if outcome == "error" || outcome == "skipped-lock" {
		if err := storage.AppendEvent(db, "consolidate_attempt", fmt.Sprintf(`{"outcome":%q}`, outcome)); err != nil {
			// No event of its own (a busy store): stamp the entry one past the
			// head, so it can never sit at or below a notice cursor and is
			// reported by the first notice after any later event.
			return consolidate.MaxEventID(db) + 1
		}
	}
	return consolidate.MaxEventID(db)
}

// withNoticeLock runs fn (an attempt's event append and log write) while
// holding notice.lock, the lock a notice holds from reading the event head
// to reading the log, so the pair is never seen half done. It waits a couple
// of seconds for a notice in flight and then goes ahead without the lock.
func withNoticeLock(cfg config.Config, fn func()) {
	for i := 0; i < 100; i++ {
		if release, ok := distill.AcquireLearnLock(cfg.StatePath("notice.lock")); ok {
			defer release()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fn()
}

// recordSkippedLock records a busy-lock attempt (req 10a): no pass ran at
// all, so there is nothing else to report beyond the outcome itself.
func recordSkippedLock(cfg config.Config, db *sql.DB, trigger string) {
	withNoticeLock(cfg, func() {
		_ = consolidate.RecordAttempt(cfg, consolidate.Attempt{
			Trigger: trigger,
			Outcome: "skipped-lock",
			EventID: attemptEventID(db, "skipped-lock"),
		})
	})
}

// combineErrors folds a prior learn error and this pass's own error into one
// message for the attempt log, without losing either half.
func combineErrors(learnErr, passErr error) error {
	switch {
	case learnErr == nil:
		return passErr
	case passErr == nil:
		return fmt.Errorf("learn: %w", learnErr)
	default:
		return fmt.Errorf("learn: %v; consolidate: %w", learnErr, passErr)
	}
}

func cmdPull() *cobra.Command {
	return &cobra.Command{
		Use:   "pull [path]",
		Short: "Reimport a TASTE.md (default ./TASTE.md) into the store as IMPORTED w<=0.4",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "TASTE.md"
			if len(args) > 0 {
				path = args[0]
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "pull"); err != nil {
				return fmt.Errorf("backup before pull: %w", err)
			} else if bak != "" {
				fmt.Fprintln(os.Stderr, "backup:", bak)
			}
			n, err := render.Reimport(db, cfg, path)
			if err != nil {
				fmt.Println("pull: nothing to reimport —", err)
				return nil
			}
			fmt.Printf("reimported %d preferences from %s (IMPORTED w<=0.4 quarantine)\n", n, path)
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
			// 'rebuild' re-derives each external-content index from its table;
			// the DELETE … MATCH '*' form is invalid on external-content FTS5.
			_, _ = db.Exec(`INSERT INTO fts_working(fts_working) VALUES('rebuild')`)
			_, _ = db.Exec(`INSERT INTO fts_episodic(fts_episodic) VALUES('rebuild')`)
			_, _ = db.Exec(`INSERT INTO fts_preferences(fts_preferences) VALUES('rebuild')`)
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
			"command_code-global (~/.commandcode/settings.json, global), zcode (.zcode/config.json, workspace),\n" +
			"zcode-global (~/.zcode/cli/config.json) and cline (~/.cline/hooks, event shims).\n" +
			"Default with no --harness: claude_code, command_code, zcode, cline.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			bin := hookBinaryPath()
			want, err := parseHarnessList(installHarnesses)
			if err != nil {
				return err
			}
			var installed []string
			for _, h := range []string{"claude_code", "command_code", "command_code-global", "zcode", "zcode-global", "cline"} {
				if !want[h] {
					continue
				}
				switch h {
				case "claude_code":
					err = adapters.WriteClaudeCodeHooks(cwd, bin)
				case "command_code":
					err = adapters.WriteCommandCodeHooks(cwd, bin)
				case "command_code-global":
					err = adapters.WriteCommandCodeHooksGlobal(bin)
				case "zcode":
					err = adapters.WriteZCodeHooks(cwd, bin)
				case "zcode-global":
					err = adapters.WriteZCodeHooksGlobal(bin)
				case "cline":
					err = adapters.WriteClineHooksGlobal(bin)
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
		"claude_code|command_code|command_code-global|zcode|zcode-global|cline (comma-separated; default: claude_code,command_code,zcode,cline)")
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
			for _, h := range []string{"claude_code", "command_code", "command_code-global", "zcode", "zcode-global", "cline"} {
				if !want[h] {
					continue
				}
				var n int
				switch h {
				case "claude_code":
					n, err = adapters.RemoveClaudeCodeHooks(cwd)
				case "command_code":
					n, err = adapters.RemoveCommandCodeHooks(cwd)
				case "command_code-global":
					n, err = adapters.RemoveCommandCodeHooksGlobal()
				case "zcode":
					n, err = adapters.RemoveZCodeHooksAt(adapters.ZCodeWorkspaceConfigPath(cwd))
				case "zcode-global":
					var p string
					if p, err = adapters.ZCodeGlobalConfigPath(); err == nil {
						n, err = adapters.RemoveZCodeHooksAt(p)
					}
				case "cline":
					n, err = adapters.RemoveClineHooksGlobal()
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
		"claude_code|command_code|command_code-global|zcode|zcode-global|cline (default: all)")
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
			if ccGlobal, err := adapters.CommandCodeGlobalSettingsPath(); err == nil {
				rows = append(rows, row{"command_code-global", ccGlobal, adapters.CountPalateHooksTopLevel(ccGlobal)})
			}
			zc := row{"zcode", adapters.ZCodeWorkspaceConfigPath(cwd), 0}
			zc.count = adapters.CountPalateHooksZCode(zc.path)
			rows = append(rows, zc)
			if globalPath, err := adapters.ZCodeGlobalConfigPath(); err == nil {
				rows = append(rows, row{"zcode-global", globalPath, adapters.CountPalateHooksZCode(globalPath)})
			}
			if clineDir, err := adapters.ClineHooksDir(); err == nil {
				rows = append(rows, row{"cline", clineDir, adapters.CountClineHooks(clineDir)})
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
				Source         string `json:"source"` // startup | resume | clear | compact
				CWD            string `json:"cwd"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			cfg := loadConfig()
			_ = cfg.EnsureLayout()
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				return nil
			}
			defer storage.Close()
			// Read back the owner's hand edits before the profile or notice is
			// built, so the model sees them in the same session (req 6).
			if err := render.ReadBack(db, cfg, cfg.TastePath()); err != nil {
				fmt.Fprintf(os.Stderr, "palate: read-back TASTE.md: %v\n", err)
			}
			handleStateWalkUserDiffs(db, cfg)
			// SessionStart spawns exactly one detached child (phase-04 req 9,
			// red-team F4): a transcript with learning on carries the due
			// pass along in the same process (`--then-consolidate`), under
			// the lock it already holds, so the old two-spawn race for
			// learn.lock is gone by construction; with no transcript, a due
			// pass still fires alone.
			if cfg.LLMConfigured() {
				_, _, due := consolidate.Due(db, cfg)
				switch {
				case input.TranscriptPath != "" && cfg.LearningOn() && !cfg.LearningSkipped(input.CWD):
					// Detached, fire-and-forget: this must never make the
					// hook wait on the distiller's (or the pass's) LLM call.
					_ = spawnLearnSession(hookBinaryPath(), input.TranscriptPath, due)
				case due:
					_ = spawnConsolidate(hookBinaryPath())
				}
			}
			out := map[string]interface{}{}
			// What changed since the previous session travels as systemMessage
			// (a conversation notice), the profile itself as additionalContext.
			// A compaction restarts the same session; "since your last session"
			// would be wrong there, so the notice waits for the next real start.
			if input.Source != "compact" {
				// The learned block is not needed here: the profile below
				// already carries every row.
				if notice, _ := learnedSinceNotice(db, cfg, "since your last session"); notice != "" {
					out["systemMessage"] = notice
				}
			}
			block, err := prewarm.SessionPrewarm(db, cfg)
			if err == nil && block.Content != "" {
				out["hookSpecificOutput"] = map[string]interface{}{
					"hookEventName":     "SessionStart",
					"additionalContext": block.Content,
				}
			}
			if len(out) == 0 {
				return nil
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
			// Read back the owner's hand edits before the profile or notice is
			// built, so the model sees them in the same session (req 6).
			if err := render.ReadBack(db, cfg, cfg.TastePath()); err != nil {
				fmt.Fprintf(os.Stderr, "palate: read-back TASTE.md: %v\n", err)
			}
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
			out := map[string]interface{}{}
			var ctxParts []string
			// What the detached learner stored since the last notice (a
			// Stop-hook distill, an MCP write from another pane) is announced
			// here — Command Code's inline "taste learned" — and handed to the
			// model, whose SessionStart profile predates it.
			if notice, learned := learnedSinceNotice(db, cfg, "since your last prompt"); notice != "" {
				out["systemMessage"] = notice
				if learned != "" {
					ctxParts = append(ctxParts, learned)
				}
			}
			if block, fired, _ := prewarm.GoalPrewarm(db, cfg, input.Prompt); fired {
				ctxParts = append(ctxParts, block.Content)
			}
			if len(ctxParts) > 0 {
				out["hookSpecificOutput"] = map[string]interface{}{
					"hookEventName":     "UserPromptSubmit",
					"additionalContext": strings.Join(ctxParts, "\n"),
				}
			}
			if len(out) == 0 {
				return nil
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
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
			cfg := loadConfig()
			_ = cfg.EnsureLayout()
			statePath := cfg.StatePath("model_edits.json")
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
				CWD            string `json:"cwd"`
				Files          []struct {
					Path      string `json:"path"`
					HashAfter string `json:"hash_after"`
				} `json:"files"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			statePath := cfg.StatePath("model_edits.json")
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
			if cfg.LLMConfigured() && input.TranscriptPath != "" && cfg.LearningOn() && !cfg.LearningSkipped(input.CWD) {
				// Detached, fire-and-forget: this must never make the hook
				// wait on the distiller's LLM call. Learn only — the dream
				// pass never rides along on Stop (req 9).
				_ = spawnLearnSession(hookBinaryPath(), input.TranscriptPath, false)
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
			path := cfg.StatePath("recovery.md")
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
		for _, h := range []string{"claude_code", "command_code", "zcode", "cline"} {
			want[h] = true
		}
	}
	valid := map[string]bool{"claude_code": true, "command_code": true, "command_code-global": true, "zcode": true, "zcode-global": true, "cline": true}
	for _, item := range list {
		for _, h := range strings.Split(item, ",") {
			h = strings.TrimSpace(h)
			if !valid[h] {
				return nil, fmt.Errorf("unknown harness %q (valid: claude_code, command_code, command_code-global, zcode, zcode-global, cline)", h)
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
//
// 2026-09-15: the verbatim learn path is retired. ExtractCandidates always
// returns nil now (file diffs are work product, not preferences — 53 verbatim
// "prefers: <code>" rows had to be bulk-retired from the live store), and the
// detached LLM distiller already covers the same edit through the transcript.
// So a drifted file is counted and cleared, never stored.
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
	statePath := cfg.StatePath("model_edits.json")
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
			p, a, err := judge.Apply(cmd.Context(), db, cfg, judge.Input{Statement: stmt, Signal: learn.SignalExplicitPrefer}, "")
			if err != nil {
				return err
			}
			fmt.Printf("prefer recorded (%s)\n", judge.Verdict(p, a))
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
			p, a, err := judge.Correct(cmd.Context(), db, cfg, c)
			if err != nil {
				return err
			}
			fmt.Printf("correct recorded (%s)\n", judge.Verdict(p, a))
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
			var source string
			_ = db.QueryRow(`SELECT source FROM proposals WHERE id=?`, args[0]).Scan(&source)
			if source == "consolidate" {
				// A consolidation action rewrites, merges or retires live rows:
				// the same destructive class as forget/pull, so it gets the
				// same backup (once per process, however many are promoted).
				if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "promote"); err != nil {
					return err
				} else if bak != "" {
					fmt.Fprintln(os.Stderr, "backup:", bak)
				}
			}
			_, err := learn.PromoteProposal(db, args[0], as, cfg, func(sig learn.Signal) error {
				if source == "consolidate" {
					// the proposal's content is the stored action, not a statement
					return consolidate.ApplyProposal(db, cfg, sig.Statement)
				}
				_, _, err := judge.Apply(cmd.Context(), db, cfg, judge.Input{Statement: sig.Statement, Signal: sig.Type, Category: sig.Category}, "")
				return err
			})
			if err != nil {
				return err
			}
			if source == "consolidate" {
				fmt.Println("applied consolidation", args[0])
			} else {
				fmt.Println("promoted", args[0], "as", as)
			}
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
			if err := skills.WriteCandidates(cfg, cands); err != nil {
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
			entries, _ := os.ReadDir(loadConfig().SkillCandidatesDir())
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
			if err := skills.InstallCandidate(loadConfig(), args[0]); err != nil {
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
			cfg := loadConfig()
			if len(args) == 0 {
				m := skills.LoadStats(cfg)
				b, _ := json.MarshalIndent(m, "", "  ")
				fmt.Println(string(b))
				for _, issue := range skills.Monitors(cfg) {
					fmt.Fprintln(os.Stderr, "monitor:", issue)
				}
				return nil
			}
			if err := skills.BumpStat(cfg, args[0], true); err != nil {
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
func learnLockPath(cfg config.Config) string {
	_ = cfg.EnsureLayout()
	return cfg.StatePath("learn.lock")
}

func cmdLearnSession() *cobra.Command {
	var harness string
	var verbose, thenConsolidate bool
	c := &cobra.Command{
		Use:          "session <path>",
		Short:        "Learn preferences from one transcript",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
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
			release, ok := distill.AcquireLearnLock(learnLockPath(cfg))
			if !ok {
				// Every learner that skips on a busy lock is visible, never
				// silent (red-team M8): the counter always bumps; the
				// then-consolidate caller (the SessionStart spawn) also
				// records the attempt log line req 10a asks for.
				storage.BumpDrop(db, "learn_skipped_lock")
				if thenConsolidate {
					recordSkippedLock(cfg, db, "session-start")
				}
				if verbose {
					fmt.Println("palate: learn already in progress — skipping")
				}
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)

			summary, learnErr := distill.LearnSession(context.Background(), db, cfg, gw, reader, distill.Transcript{Harness: h, Path: path})
			if !thenConsolidate {
				if learnErr != nil {
					return learnErr
				}
				printLearnOutcome(summary)
				if verbose {
					fmt.Println("gateway model:", cfg.LLM.Model)
				}
				return learnFailure(summary)
			}

			// --then-consolidate: a learn error never blocks the due pass
			// (req 10a) — it still runs, under the lock this process already
			// holds, and the learn error rides into the attempt's Error.
			if learnErr == nil {
				printLearnOutcome(summary)
				if verbose {
					fmt.Println("gateway model:", cfg.LLM.Model)
				}
			}
			if _, _, due := consolidate.Due(db, cfg); due {
				if _, cerr := runConsolidatePass(cmd.Context(), cfg, db, "session-start", consolidate.RunOptions{}, learnErr); cerr != nil {
					fmt.Fprintln(os.Stderr, "palate: consolidate:", cerr)
				}
			}
			if learnErr != nil {
				return learnErr
			}
			return learnFailure(summary)
		},
	}
	c.Flags().StringVar(&harness, "harness", "", "harness override: claude_code|command_code|cline|pi|codex|opencode|gemini_cli|memsearch|zcode")
	c.Flags().BoolVar(&verbose, "verbose", false, "print the gateway model")
	c.Flags().BoolVar(&thenConsolidate, "then-consolidate", false, "after learning, run the due dream pass in this same process (holds learn.lock throughout)")
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
			release, ok := distill.AcquireLearnLock(learnLockPath(cfg))
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
			release, ok := distill.AcquireLearnLock(learnLockPath(cfg))
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
		Use:          "repo [path|owner/repo|url]",
		Short:        "Learn preferences from a git repository's commit history (local path, GitHub owner/repo, or clone URL)",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
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
			var repoPath, cursorKey string
			if url, remote := distill.RepoSource(path); remote {
				// Command Code parity: `owner/repo` or a clone URL is fetched
				// into a temp dir, learned, and removed; the cursor lives under
				// the URL so a second run only sees new commits.
				dir, cleanup, err := distill.CloneSource(cmd.Context(), url)
				if err != nil {
					return err
				}
				defer cleanup()
				repoPath, cursorKey = dir, url
			} else {
				abs, err := filepath.Abs(path)
				if err != nil {
					return fmt.Errorf("resolve path: %w", err)
				}
				repoPath, cursorKey = abs, abs
			}

			author := ""
			if cursorKey != repoPath && !allAuthors {
				// A fresh clone answers `git config user.email` with the
				// operator's global identity, which proves nothing about who
				// committed there; a remote source learns from every author.
				fmt.Fprintln(os.Stderr, "palate: learn repo: remote source — learning from all authors")
				allAuthors = true
			}
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
			release, ok := distill.AcquireLearnLock(learnLockPath(cfg))
			if !ok {
				if verbose {
					fmt.Println("palate: learn already in progress — skipping")
				}
				return nil
			}
			defer release()
			gw, _ := distill.NewGatewayFromEnv(cfg)

			signalCount, summary, err := distill.LearnRepoKeyed(cmd.Context(), db, cfg, gw, repoPath, cursorKey, author, maxCommits, maxSignals)
			if err != nil {
				return err
			}
			fmt.Printf("%d signals -> %d preferences (%d applied, %d dropped)\n", signalCount, summary.Applied+summary.Dropped, summary.Applied, summary.Dropped)
			if verbose {
				fmt.Println("gateway model:", cfg.LLM.Model)
			}
			return learnFailure(summary)
		},
	}
	c.Flags().IntVar(&maxCommits, "max-commits", 200, "maximum commits to scan")
	c.Flags().IntVar(&maxSignals, "max-signals", 50, "maximum before/after signals to extract")
	c.Flags().BoolVar(&allAuthors, "all-authors", false, "learn from every commit author, not just git config user.email")
	c.Flags().BoolVar(&verbose, "verbose", false, "print the gateway model")
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

// learnFailure turns a non-advancing run (the gateway failed and the cursor
// stayed put) into a visible, non-zero outcome: a dead gateway must never
// read like "nothing worth learning". A forced abandon is reported too, as a
// warning, since the window is lost. Skipped and clean runs return nil.
func learnFailure(s distill.Summary) error {
	if s.Skipped {
		return nil
	}
	if s.Abandoned {
		fmt.Fprintf(os.Stderr, "palate: learn: window abandoned after repeated failures (%s)\n", s.Reason)
		return nil
	}
	if !s.Advanced {
		return fmt.Errorf("learn: not advanced — %s (cursor kept; rerun when the gateway is back)", s.Reason)
	}
	return nil
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
		{"cline", filepath.Join(home, ".cline", "data", "sessions")},
	}
	for _, r := range roots {
		if r.root != "" && strings.HasPrefix(abs, r.root) {
			return r.harness, nil
		}
	}
	return "", fmt.Errorf("cannot infer harness from path %q; pass --harness (claude_code|command_code|cline|pi|codex|opencode|gemini_cli|memsearch|zcode)", path)
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
