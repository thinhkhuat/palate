package distill

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

func TestParseResponseRulebookFlag(t *testing.T) {
	stmts, err := ParseResponse(`{"preferences":[{"statement":"Prefers table-driven tests.","category":"Testing","confidence":0.8,"rulebook":true,"evidence":"e"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 || !stmts[0].Rulebook {
		t.Fatalf("the optional rulebook flag must parse: %+v (err %v)", stmts, err)
	}
	stmts, err = ParseResponse(`{"preferences":[{"statement":"Prefers tabs.","category":"Editing","confidence":0.8,"evidence":"e"}]}`)
	if err != nil || len(stmts) != 1 || stmts[0].Rulebook {
		t.Fatalf("an absent flag must default to false: %+v (err %v)", stmts, err)
	}
}

func TestBuildUserPromptRulebookSection(t *testing.T) {
	out := BuildUserPrompt("prefs", "prev", "new", "Always lint before commit.")
	if !strings.Contains(out, "## CURATED RULEBOOK") || !strings.Contains(out, "Always lint before commit.") {
		t.Fatalf("a configured rulebook must ride along: %s", out)
	}
	out = BuildUserPrompt("prefs", "prev", "new", "")
	if strings.Contains(out, "CURATED RULEBOOK") {
		t.Fatal("no configured rulebook: the section must be omitted")
	}
}

func TestGatewayStampsLastCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"preferences\":[]}"}}]}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	cfg := config.Config{Home: home}
	cfg.LLM.Endpoint = srv.URL + "/v1/chat/completions"
	cfg.LLM.Model = "m1"
	gw, ok := NewGatewayFromEnv(cfg)
	if !ok {
		t.Fatal("gateway must build from a configured env")
	}
	if _, err := gw.Complete(context.Background(), "sys", "user", 0, nil); err != nil {
		t.Fatal(err)
	}
	model, at, ok := LastGatewayStamp(cfg)
	if !ok || model != "m1" || at == "" {
		t.Fatalf("a successful call must stamp state/last_gateway.json: model=%q at=%q ok=%v", model, at, ok)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "last_gateway.json")); err != nil {
		t.Fatalf("stamp file missing: %v", err)
	}
}
