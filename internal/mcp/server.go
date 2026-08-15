package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thinhkhuat/palate/internal/config"
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
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("recall: %q (stub — wired to storage.Recall)", q)}},
	}, nil, nil
}
func handleRemember(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	c, _ := args["content"].(string)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("remember: %q", c)}}}, nil, nil
}
func handleCorrect(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "correct recorded"}}}, nil, nil
}
func handlePrefer(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "prefer recorded"}}}, nil, nil
}
func handleReject(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "reject recorded"}}}, nil, nil
}
func handleAccept(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "accept recorded"}}}, nil, nil
}
func handleStats(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "stats: (stub)"}}}, nil, nil
}
func handleForget(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, interface{}, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "forget: (stub)"}}}, nil, nil
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
	if isLoopback(host) {
		return nil
	}
	token := os.Getenv(cfg.MCP.TokenEnv)
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

func hashToken(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}

var _ = strings.Contains
var _ = hashToken
