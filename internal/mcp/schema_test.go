package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thinhkhuat/palate/internal/config"
)

// listTools connects an in-memory client to a fresh server and returns tools/list.
func listTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	srv := NewServer(config.Default())
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	return res.Tools
}

type wireSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func decodeSchema(t *testing.T, tool *mcp.Tool) wireSchema {
	t.Helper()
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("%s: marshal schema: %v", tool.Name, err)
	}
	var s wireSchema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%s: decode schema: %v", tool.Name, err)
	}
	return s
}

func TestToolSchemas(t *testing.T) {
	tools := listTools(t)
	if len(tools) != 8 {
		t.Fatalf("expected 8 tools, got %d", len(tools))
	}
	want := map[string][]string{
		"palate_recall":   {"query"},
		"palate_remember": {"content"},
		"palate_correct":  {"tried", "wrong_because", "fix", "trigger_context"},
		"palate_prefer":   {"statement"},
		"palate_reject":   {"statement"},
		"palate_accept":   {"statement"},
		"palate_forget":   {"id"},
		"palate_stats":    {}, // takes no arguments; an empty object schema is the honest contract
	}
	for _, tool := range tools {
		props, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %s", tool.Name)
			continue
		}
		s := decodeSchema(t, tool)
		if len(props) > 0 && len(s.Properties) == 0 {
			t.Errorf("tool %s advertises an empty input schema", tool.Name)
			continue
		}
		for _, p := range props {
			if _, ok := s.Properties[p]; !ok {
				t.Errorf("tool %s: schema is missing property %q", tool.Name, p)
			}
		}
	}
}
