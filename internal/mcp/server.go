package mcp

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/judge"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/render"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

func NewServer(cfg config.Config) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "palate", Version: "0.6.0"}, nil)
	registerTools(s)
	return s
}

// Tool inputs. The go-sdk derives each tool's JSON schema from these structs:
// the json name is the property, the jsonschema tag is its description, and a
// field without omitempty is required.
type RecallInput struct {
	Query string `json:"query" jsonschema:"search text matched against preferences and memories"`
}

type RememberInput struct {
	Content string `json:"content" jsonschema:"memory content to store"`
}

type CorrectInput struct {
	Tried          string `json:"tried" jsonschema:"what was tried"`
	WrongBecause   string `json:"wrong_because,omitempty" jsonschema:"why it was wrong"`
	Fix            string `json:"fix" jsonschema:"the corrected approach; this is what gets learned"`
	TriggerContext string `json:"trigger_context,omitempty" jsonschema:"the situation in which this correction applies"`
}

type StatementInput struct {
	Statement string `json:"statement" jsonschema:"the preference statement"`
}

type StatsInput struct{}

type ForgetInput struct {
	ID string `json:"id" jsonschema:"id of the memory or preference to forget, as printed by palate_recall; a preference is retired, not erased, and the owner can restore it with the palate restore command"`
}

// flushed wraps a write tool so TASTE.md is re-rendered right after the
// call: the server is long-lived and never reaches storage.Close, which is
// where every other process flushes.
func flushed[In any](h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, interface{}, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, interface{}, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, interface{}, error) {
		res, out, err := h(ctx, req, in)
		storage.FlushIfDirty()
		return res, out, err
	}
}

func registerTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "palate_recall", Description: "Recall preferences and memories matching a query; a preference that is also in the budgeted taste profile (TASTE.md) is marked [profile]"}, handleRecall)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_remember", Description: "Store a memory"}, flushed(handleRemember))
	mcp.AddTool(s, &mcp.Tool{Name: "palate_correct", Description: "Record a correction: tried, wrong_because, fix, trigger_context"}, flushed(handleCorrect))
	mcp.AddTool(s, &mcp.Tool{Name: "palate_prefer", Description: "Record an explicit preference"}, flushed(handlePrefer))
	mcp.AddTool(s, &mcp.Tool{Name: "palate_reject", Description: "Record an explicit rejection"}, flushed(handleReject))
	mcp.AddTool(s, &mcp.Tool{Name: "palate_accept", Description: "Record an explicit acceptance"}, flushed(handleAccept))
	mcp.AddTool(s, &mcp.Tool{Name: "palate_stats", Description: "Show capture and storage stats"}, handleStats)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_forget", Description: "Forget a memory (deleted) or a preference (retired, restorable with the palate restore command; there is no MCP restore tool) by id"}, flushed(handleForget))
}

func textResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}, IsError: true}
}

// openStore loads config and opens the DB, returning a tool error instead of a
// nil db when either fails.
func openStore() (config.Config, *sql.DB, *mcp.CallToolResult) {
	// Same resolver as the CLI (config.Resolve): the MCP server and the
	// hooks must never disagree on which store they talk to.
	cfg, err := config.Resolve("")
	if err != nil {
		return cfg, nil, errResult(fmt.Sprintf("config error: %v", err))
	}
	_ = cfg.EnsureLayout()
	render.RegisterAuto(cfg)
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		return cfg, nil, errResult(fmt.Sprintf("db error: %v", err))
	}
	return cfg, db, nil
}

// gateExplicit runs the full safety scan on content that will be stored at the
// STATED tier and pushed unredacted into prewarm. Flagged content is rejected,
// never stored cleaned.
func gateExplicit(s string) *mcp.CallToolResult {
	if s == "" {
		return errResult("missing statement")
	}
	if scan := safety.Scan(s); scan.Flagged {
		return errResult(fmt.Sprintf("rejected: %s pattern", scan.Reason))
	}
	return nil
}

func handleRecall(ctx context.Context, req *mcp.CallToolRequest, in RecallInput) (*mcp.CallToolResult, interface{}, error) {
	cfg, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	results, _ := storage.Recall(db, in.Query, 10, false)
	if len(results) == 0 {
		return textResult("no results"), nil, nil
	}
	markProfileMembers(db, cfg, results)
	var lines []string
	for _, r := range results {
		marker := ""
		if r.Table == "preferences" && r.InProfile {
			marker = " [profile]"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s (%.2f)%s id=%s", r.Table, r.Content, r.Score, marker, r.ID))
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

// markProfileMembers sets InProfile on every preference among results,
// computed with the same learn.SelectProfile the SessionStart block uses
// (phase-02 req 14) — palate_recall keeps searching the whole live ledger;
// this only annotates which of the hits also made the budgeted profile.
func markProfileMembers(db *sql.DB, cfg config.Config, results []storage.RecallResult) {
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

func handleRemember(ctx context.Context, req *mcp.CallToolRequest, in RememberInput) (*mcp.CallToolResult, interface{}, error) {
	c := in.Content
	scan := safety.Scan(c)
	if scan.Flagged && scan.Reason == "secret" {
		return errResult("rejected: secret pattern"), nil, nil
	}
	if scan.Flagged {
		c = scan.Cleaned
	}
	_, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	id, err := storage.Remember(db, c, "", 0.5, "working_memory")
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	storage.BumpHit(db, "explicit_remember")
	return textResult(id), nil, nil
}

func handleCorrect(ctx context.Context, req *mcp.CallToolRequest, in CorrectInput) (*mcp.CallToolResult, interface{}, error) {
	c := learn.Correction{Tried: in.Tried, WrongBecause: in.WrongBecause, Fix: in.Fix, TriggerContext: in.TriggerContext}
	if fail := gateExplicit(c.Statement()); fail != nil {
		return fail, nil, nil
	}
	for _, txt := range c.Texts() {
		if txt == "" {
			continue
		}
		if fail := gateExplicit(txt); fail != nil {
			return fail, nil, nil
		}
	}
	cfg, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	if _, _, err := judge.Correct(context.Background(), db, cfg, c); err != nil {
		return errResult(err.Error()), nil, nil
	}
	storage.BumpHit(db, "explicit_correction")
	return textResult("correct recorded: " + c.Statement()), nil, nil
}

// recordStatement is the shared body of prefer, reject and accept.
func recordStatement(stmt string, sig learn.SignalType, counter, label string) (*mcp.CallToolResult, interface{}, error) {
	if fail := gateExplicit(stmt); fail != nil {
		return fail, nil, nil
	}
	cfg, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	p, a, err := judge.Apply(context.Background(), db, cfg, judge.Input{Statement: stmt, Signal: sig}, "")
	if err != nil {
		return errResult(err.Error()), nil, nil
	}
	storage.BumpHit(db, counter)
	return textResult(fmt.Sprintf("%s recorded: %s (%s)", label, p.Statement, judge.Verdict(p, a))), nil, nil
}

func handlePrefer(ctx context.Context, req *mcp.CallToolRequest, in StatementInput) (*mcp.CallToolResult, interface{}, error) {
	return recordStatement(in.Statement, learn.SignalExplicitPrefer, "explicit_prefer", "prefer")
}

func handleReject(ctx context.Context, req *mcp.CallToolRequest, in StatementInput) (*mcp.CallToolResult, interface{}, error) {
	return recordStatement(in.Statement, learn.SignalExplicitReject, "explicit_reject", "reject")
}

func handleAccept(ctx context.Context, req *mcp.CallToolRequest, in StatementInput) (*mcp.CallToolResult, interface{}, error) {
	return recordStatement(in.Statement, learn.SignalExplicitAccept, "explicit_accept", "accept")
}

func handleStats(ctx context.Context, req *mcp.CallToolRequest, in StatsInput) (*mcp.CallToolResult, interface{}, error) {
	_, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	var wm, em, prefs, events int
	_ = db.QueryRow(`SELECT count(*) FROM working_memory`).Scan(&wm)
	_ = db.QueryRow(`SELECT count(*) FROM episodic_memory`).Scan(&em)
	_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&prefs)
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	return textResult(fmt.Sprintf("working: %d episodic: %d preferences: %d events: %d", wm, em, prefs, events)), nil, nil
}

func handleForget(ctx context.Context, req *mcp.CallToolRequest, in ForgetInput) (*mcp.CallToolResult, interface{}, error) {
	if in.ID == "" {
		return errResult("missing id"), nil, nil
	}
	_, db, fail := openStore()
	if fail != nil {
		return fail, nil, nil
	}
	if err := storage.Forget(db, in.ID); err != nil {
		return errResult(err.Error()), nil, nil
	}
	return textResult("forgot " + in.ID), nil, nil
}

func ServeStdio(cfg config.Config) error {
	srv := NewServer(cfg)
	return srv.Run(context.Background(), newStdioTransport())
}

func ServeSSE(cfg config.Config, addr string) error {
	if err := checkAuthGuard(cfg, addr); err != nil {
		return err
	}
	srv := NewServer(cfg)
	h := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server { return srv }, nil)
	return http.ListenAndServe(addr, requireBearerToken(cfg, h))
}

// requireBearerToken enforces the token on every request once a token is
// configured. checkAuthGuard only runs at bind time; without this layer every
// request after binding was served unchecked.
func requireBearerToken(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := os.Getenv(cfg.MCP.TokenEnv); token != "" {
			const prefix = "Bearer "
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, prefix) || !CheckToken(strings.TrimPrefix(auth, prefix), token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func checkAuthGuard(cfg config.Config, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	token := os.Getenv(cfg.MCP.TokenEnv)
	if isLoopback(host) {
		if host == "" && token == "" {
			return fmt.Errorf("refusing to bind %s without %s: empty host binds all interfaces, use 127.0.0.1 explicitly or set a token", addr, cfg.MCP.TokenEnv)
		}
		return nil
	}
	if token == "" {
		return fmt.Errorf("refusing to bind %s without %s (loopback only without token)", addr, cfg.MCP.TokenEnv)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "ip6-localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func CheckToken(provided, expected string) bool {
	return hmac.Equal([]byte(provided), []byte(expected)) && expected != ""
}
