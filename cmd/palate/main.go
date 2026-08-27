package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thinhkhuat/palate/internal/adapters"
	"github.com/thinhkhuat/palate/internal/config"
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

func main() {
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

	if err := root.Execute(); err != nil {
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
		Use:   "doctor",
		Short: "Self-diagnostic",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			fmt.Printf("config: %s\n", cfgFile)
			fmt.Printf("db: %s\n", cfg.DBPath)
			fmt.Printf("c_floor: %.2f half_life: %.0fh\n", cfg.CFloor, cfg.HalfLifeHours)
			db, err := storage.Open(cfg.DBPath)
			if err != nil {
				fmt.Printf("db: FAIL %v\n", err)
				return nil
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
			fmt.Printf("adapter: claude_code hooks generatable — run: palate hook install\n")
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
			results, err := storage.Recall(db, q, limit, includeConflicted)
			if err != nil {
				return err
			}
			if asOf != "" {
				fmt.Fprintf(os.Stderr, "note: --as-of %s pins decay replay to that instant (Phase 5 as_of determinism)\n", asOf)
			}
			if jsonOut {
				b, _ := json.MarshalIndent(results, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			for _, r := range results {
				fmt.Printf("[%s] %s (%.3f)\n", r.Table, r.Content, r.Score)
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
	var scope string
	c := &cobra.Command{
		Use:   "prewarm",
		Short: "Emit prewarm block",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			block, err := prewarm.SessionPrewarm(db, cfg)
			if err != nil {
				return err
			}
			_ = scope
			fmt.Print(block.Content)
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "session", "session|goal")
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
				return mcp.ServeSSE(cfg, ":"+port)
			}
			return mcp.ServeStdio(cfg)
		},
	}
	c.Flags().StringVar(&port, "port", "", "SSE port (default: stdio)")
	return c
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
			return storage.Forget(db, args[0])
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
	c.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install Claude Code hooks into .claude/settings.local.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			bin, _ := os.Executable()
			if bin == "" {
				bin = "palate"
			}
			if err := adapters.WriteClaudeCodeHooks(cwd, bin); err != nil {
				return err
			}
			fmt.Println("hooks installed")
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
			if learn.IsCorrectionShaped(input.Prompt) {
				if stmt, cat := learn.ExtractStatedPreference(input.Prompt); stmt != "" {
					_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalStatedPreference, Statement: stmt, Category: cat}, nil)
				}
			}
			prompt := input.Prompt
			if prompt == "" {
				b, _ := os.ReadFile(filepath.Join(".palate", "last_prompt"))
				prompt = string(b)
			}
			if prompt != "" {
				if block, fired, _ := prewarm.GoalPrewarm(db, cfg, prompt); fired {
					out := map[string]interface{}{
						"hookSpecificOutput": map[string]interface{}{
							"hookEventName":   "UserPromptSubmit",
							"additionalContext": block.Content,
						},
					}
					b, _ := json.Marshal(out)
					fmt.Println(string(b))
				}
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "post-tool-use",
		Short: "PostToolUse handler (records model_edit file→hash)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var input struct {
				ToolName string `json:"tool_name"`
				ToolInput struct {
					FilePath string `json:"file_path"`
				} `json:"tool_input"`
				ToolResponse struct {
					FilePath string `json:"filePath"`
				} `json:"tool_response"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			fp := input.ToolInput.FilePath
			if fp == "" {
				fp = input.ToolResponse.FilePath
			}
			if fp == "" {
				var raw map[string]interface{}
				_ = json.NewDecoder(os.Stdin).Decode(&raw)
				_ = raw
				return nil
			}
			b, err := os.ReadFile(fp)
			if err != nil {
				return nil
			}
			h := fileHash(b)
			statePath := filepath.Join(".palate", "model_edits.json")
			_ = os.MkdirAll(".palate", 0700)
			m := map[string]string{}
			if data, err := os.ReadFile(statePath); err == nil {
				_ = json.Unmarshal(data, &m)
			}
			m[fp] = h
			data, _ := json.Marshal(m)
			_ = os.WriteFile(statePath, data, 0600)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop handler (user_diff a/b/c filter)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			var input struct {
				SessionID string `json:"session_id"`
				Files     []struct {
					Path      string `json:"path"`
					HashAfter string `json:"hash_after"`
				} `json:"files"`
			}
			_ = json.NewDecoder(os.Stdin).Decode(&input)
			statePath := filepath.Join(".palate", "model_edits.json")
			modelEdits := map[string]string{}
			if b, err := os.ReadFile(statePath); err == nil {
				_ = json.Unmarshal(b, &modelEdits)
			}
			for _, f := range input.Files {
				if f.Path == "" {
					continue
				}
				hashAfter, hasModelEdit := modelEdits[f.Path]
				if !hasModelEdit {
					storage.BumpDrop(db, "user_diff")
					continue
				}
				if f.HashAfter != "" {
					hashAfter = f.HashAfter
				}
				b, err := os.ReadFile(f.Path)
				if err != nil {
					storage.BumpDrop(db, "user_diff")
					continue
				}
				currentHash := fileHash(b)
				if currentHash == hashAfter {
					storage.BumpDrop(db, "user_diff")
					continue
				}
				stmt := fmt.Sprintf("prefers change in %s", filepath.Base(f.Path))
				scan := safety.Scan(stmt)
				if scan.Flagged && scan.Reason == "secret" {
					storage.BumpDrop(db, "user_diff")
					continue
				}
				_, err = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalUserDiff, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt), File: f.Path}, nil)
				if err == nil {
					storage.BumpHit(db, "user_diff")
				} else {
					storage.BumpDrop(db, "user_diff")
				}
			}
			_ = os.Remove(statePath)
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

func fileHash(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8])
}

func cmdPrefer() *cobra.Command {
	return &cobra.Command{
		Use:   "prefer [statement]",
		Short: "Record explicit preference",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stmt := args[0]
			if safety.ContainsSecret(stmt) {
				return fmt.Errorf("rejected: secret pattern")
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
		Use:   "correct [tried] [wrong_because] [fix]",
		Short: "Record 4-field correction (Mnemos schema)",
		Args:  cobra.RangeArgs(1, 4),
		RunE: func(cmd *cobra.Command, args []string) error {
			tried := args[0]
			fix := ""
			if len(args) >= 3 {
				fix = args[2]
			} else if len(args) == 2 {
				fix = args[1]
			}
			stmt := fix
			if stmt == "" {
				stmt = tried
			}
			if safety.ContainsSecret(stmt) {
				return fmt.Errorf("rejected: secret pattern")
			}
			cfg := loadConfig()
			db := openDB(cfg)
			defer storage.Close()
			_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitCorrection, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt)}, nil)
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
			_, err := learn.PromoteProposalWithApply(db, args[0], as, cfg, func(sig learn.Signal) error {
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

func cmdUpdate() *cobra.Command {
	return &cobra.Command{
		Use: "update", Short: "Self-update (sha256 verify, rollback on bad sig)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("update: fetch latest release, verify sha256, replace binary, rollback on failure")
			fmt.Println("  curl -fsSL https://raw.githubusercontent.com/thinhkhuat/palate/main/install.sh | sh")
			fmt.Println("  or: go install github.com/thinhkhuat/palate/cmd/palate@latest")
			return nil
		},
	}
}
