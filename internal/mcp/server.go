package mcp

import (
	"context"
	"crypto/hmac"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

func NewServer(cfg config.Config) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "palate", Version: "0.1.0"}, nil)
	registerTools(s)
	return s
}

func registerTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "palate_recall", Description: "Recall preferences and memories matching a query"}, handleRecall)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_remember", Description: "Store a memory"}, handleRemember)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_correct", Description: "Record a correction: tried, wrong_because, fix, trigger_context"}, handleCorrect)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_prefer", Description: "Record an explicit preference"}, handlePrefer)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_reject", Description: "Record an explicit rejection"}, handleReject)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_accept", Description: "Record an explicit acceptance"}, handleAccept)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_stats", Description: "Show capture and storage stats"}, handleStats)
	mcp.AddTool(s, &mcp.Tool{Name: "palate_forget", Description: "Forget a memory by id"}, handleForget)
}

func handleRecall(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	q, _ := args["query"].(string)
	cfg, _ := config.Load("config.yaml")
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("db error: %v", err)}}, IsError: true}, nil, nil
	}
	results, _ := storage.Recall(db, q, 10, false)
	if len(results) == 0 {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "no results"}}, IsError: false}, nil, nil
	}
	var lines []string
	for _, r := range results {
		lines = append(lines, fmt.Sprintf("- [%s] %s (%.2f)", r.Table, r.Content, r.Score))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(lines, "\n")}}}, nil, nil
}

func handleRemember(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	c, _ := args["content"].(string)
	if safety.ContainsSecret(c) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rejected: secret pattern"}}, IsError: true}, nil, nil
	}
	scan := safety.Scan(c)
	if scan.Flagged && scan.Reason == "injection" {
		c = scan.Cleaned
	}
	cfg, _ := config.Load("config.yaml")
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("db error: %v", err)}}, IsError: true}, nil, nil
	}
	id, err := storage.Remember(db, c, "", 0.5, "working_memory")
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	storage.BumpHit(db, "explicit_remember")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: id}}}, nil, nil
}

func handleCorrect(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	tried, _ := args["tried"].(string)
	fix, _ := args["fix"].(string)
	if fix == "" {
		fix, _ = args["statement"].(string)
	}
	if fix == "" {
		fix = tried
	}
	if fix == "" {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "missing fix/statement"}}, IsError: true}, nil, nil
	}
	if safety.ContainsSecret(fix) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rejected: secret"}}, IsError: true}, nil, nil
	}
	cfg, _ := config.Load("config.yaml")
	db, _ := storage.Open(cfg.DBPath)
	_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitCorrection, Statement: fix, Category: learn.InferCategoryFromStatement(fix)}, nil)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	storage.BumpHit(db, "explicit_correction")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "correct recorded: " + fix}}}, nil, nil
}

func handlePrefer(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	stmt, _ := args["statement"].(string)
	if stmt == "" {
		stmt, _ = args["content"].(string)
	}
	if stmt == "" {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "missing statement"}}, IsError: true}, nil, nil
	}
	if safety.ContainsSecret(stmt) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rejected: secret"}}, IsError: true}, nil, nil
	}
	cfg, _ := config.Load("config.yaml")
	db, _ := storage.Open(cfg.DBPath)
	_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt)}, nil)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	storage.BumpHit(db, "explicit_prefer")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "prefer recorded: " + stmt}}}, nil, nil
}

func handleReject(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	stmt, _ := args["statement"].(string)
	if stmt == "" {
		stmt, _ = args["content"].(string)
	}
	if stmt == "" {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "missing statement"}}, IsError: true}, nil, nil
	}
	cfg, _ := config.Load("config.yaml")
	db, _ := storage.Open(cfg.DBPath)
	_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitReject, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt)}, nil)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	storage.BumpHit(db, "explicit_reject")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "reject recorded: " + stmt}}}, nil, nil
}

func handleAccept(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	stmt, _ := args["statement"].(string)
	if stmt == "" {
		stmt, _ = args["content"].(string)
	}
	if stmt == "" {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "missing statement"}}, IsError: true}, nil, nil
	}
	cfg, _ := config.Load("config.yaml")
	db, _ := storage.Open(cfg.DBPath)
	_, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitAccept, Statement: stmt, Category: learn.InferCategoryFromStatement(stmt)}, nil)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	storage.BumpHit(db, "explicit_accept")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "accept recorded: " + stmt}}}, nil, nil
}

func handleStats(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	cfg, _ := config.Load("config.yaml")
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("db error: %v", err)}}, IsError: true}, nil, nil
	}
	var wm, em, prefs, events int
	_ = db.QueryRow(`SELECT count(*) FROM working_memory`).Scan(&wm)
	_ = db.QueryRow(`SELECT count(*) FROM episodic_memory`).Scan(&em)
	_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&prefs)
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	msg := fmt.Sprintf("working: %d episodic: %d preferences: %d events: %d", wm, em, prefs, events)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
}

func handleForget(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "missing id"}}, IsError: true}, nil, nil
	}
	cfg, _ := config.Load("config.yaml")
	db, _ := storage.Open(cfg.DBPath)
	if err := storage.Forget(db, id); err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "forgot " + id}}}, nil, nil
}

func ServeStdio(cfg config.Config) error {
	srv := NewServer(cfg)
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func ServeSSE(cfg config.Config, addr string) error {
	if err := checkAuthGuard(cfg, addr); err != nil {
		return err
	}
	srv := NewServer(cfg)
	h := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server { return srv }, nil)
	return http.ListenAndServe(addr, h)
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
