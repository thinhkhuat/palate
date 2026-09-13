package distill

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

func gatewayTestConfig(endpoint, model, apiKey string) config.Config {
	cfg := config.Default()
	cfg.LLM.Endpoint = endpoint
	cfg.LLM.Model = model
	cfg.LLM.APIKey = apiKey
	return cfg
}

const testAPIKey = "sk-test-should-never-leak-1234567890"

func newTestGateway(t *testing.T, server *httptest.Server, apiKey string) *Gateway {
	t.Helper()
	return &Gateway{
		Endpoint:   server.URL,
		Model:      "test-model",
		APIKey:     apiKey,
		HTTPClient: server.Client(),
	}
}

func jsonHandler(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestGatewayComplete(t *testing.T) {
	server := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"hello world"}}]}`))
	defer server.Close()

	g := newTestGateway(t, server, "")
	content, err := g.Complete(context.Background(), "system", "user", 0, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if content != "hello world" {
		t.Fatalf("content = %q, want %q", content, "hello world")
	}
}

func TestGatewayErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, "server exploded"},
		{"malformed json", http.StatusOK, "{not json"},
		{"empty choices", http.StatusOK, `{"choices":[]}`},
	}
	for _, tc := range cases {
		func() {
			server := httptest.NewServer(jsonHandler(t, tc.status, tc.body))
			defer server.Close()

			g := newTestGateway(t, server, "")
			content, err := g.Complete(context.Background(), "system", "user", 0, nil)
			if err == nil {
				t.Errorf("%s: Complete: expected error, got content %q", tc.name, content)
			}
			if content != "" {
				t.Errorf("%s: content = %q, want empty on error", tc.name, content)
			}
		}()
	}
}

func TestGatewayRefusedFast(t *testing.T) {
	g := &Gateway{
		Endpoint:   "http://127.0.0.1:1",
		Model:      "test-model",
		HTTPClient: &http.Client{Timeout: requestTimeout},
	}

	start := time.Now()
	_, err := g.Complete(context.Background(), "system", "user", 0, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Complete: expected error against refused port")
	}
	if elapsed >= time.Second {
		t.Fatalf("Complete took %v, want well under 1s (connection refused, not a timeout wait)", elapsed)
	}
}

// captureRequests records the decoded chatRequest of every call made to it,
// always replying with the same assistant content.
type captureRequests struct {
	mu       sync.Mutex
	requests []chatRequest
	content  string
}

func (c *captureRequests) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.requests = append(c.requests, req)
		c.mu.Unlock()

		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: c.content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func (c *captureRequests) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func TestGatewayRetryOnInvalid(t *testing.T) {
	t.Run("succeeds on retry", func(t *testing.T) {
		rec := &captureRequests{content: "not-json"}
		server := httptest.NewServer(rec.handler())
		defer server.Close()

		g := newTestGateway(t, server, "")
		var calls int
		validate := func(raw string) error {
			calls++
			if calls == 1 {
				return errors.New("invalid JSON")
			}
			return nil
		}

		content, err := g.Complete(context.Background(), "system", "user", 1, validate)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if content != "not-json" {
			t.Fatalf("content = %q, want %q", content, "not-json")
		}
		if got := rec.callCount(); got != 2 {
			t.Fatalf("HTTP calls = %d, want 2", got)
		}

		second := rec.requests[1]
		if len(second.Messages) != 4 {
			t.Fatalf("second request has %d messages, want 4 (system, user, assistant, corrective user)", len(second.Messages))
		}
		if second.Messages[2].Role != "assistant" || second.Messages[2].Content != "not-json" {
			t.Fatalf("second request message[2] = %+v, want the rejected assistant reply", second.Messages[2])
		}
		if second.Messages[3].Role != "user" || !strings.Contains(second.Messages[3].Content, "Your previous reply was not valid JSON") {
			t.Fatalf("second request message[3] = %+v, want the corrective message", second.Messages[3])
		}
	})

	t.Run("exhausts retries", func(t *testing.T) {
		rec := &captureRequests{content: "still-not-json"}
		server := httptest.NewServer(rec.handler())
		defer server.Close()

		g := newTestGateway(t, server, "")
		validate := func(raw string) error { return errors.New("always invalid") }

		content, err := g.Complete(context.Background(), "system", "user", 1, validate)
		if err == nil {
			t.Fatalf("Complete: expected error after exhausting retries")
		}
		if content != "still-not-json" {
			t.Fatalf("content = %q, want the last raw content %q", content, "still-not-json")
		}
		if got := rec.callCount(); got != 2 {
			t.Fatalf("HTTP calls = %d, want retries+1 = 2", got)
		}
	})
}

func TestGatewayHTTPWarning(t *testing.T) {
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	g, ok := NewGatewayFromEnv(gatewayTestConfig("http://198.51.100.1/v1/chat/completions", "test-model", ""))

	_ = w.Close()
	os.Stderr = orig
	captured, _ := io.ReadAll(r)

	if !ok || g == nil {
		t.Fatalf("NewGatewayFromEnv: expected a usable Gateway despite the plaintext warning")
	}

	lines := strings.Split(strings.TrimRight(string(captured), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stderr = %q, want exactly one warning line", captured)
	}
	if !strings.Contains(lines[0], "PALATE_LLM_ENDPOINT uses http://") {
		t.Fatalf("warning line = %q, missing expected text", lines[0])
	}
}

func TestGatewayNeverLeaksKey(t *testing.T) {
	server := httptest.NewServer(jsonHandler(t, http.StatusInternalServerError, "server exploded"))
	defer server.Close()

	g := newTestGateway(t, server, testAPIKey)
	_, err := g.Complete(context.Background(), "system", "user", 0, nil)
	if err == nil {
		t.Fatalf("Complete: expected error from the 500 response")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error leaked the API key: %v", err)
	}

	refused := &Gateway{
		Endpoint:   "http://127.0.0.1:1",
		Model:      "test-model",
		APIKey:     testAPIKey,
		HTTPClient: &http.Client{Timeout: requestTimeout},
	}
	_, err = refused.Complete(context.Background(), "system", "user", 0, nil)
	if err == nil {
		t.Fatalf("Complete: expected error against refused port")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("refused-connection error leaked the API key: %v", err)
	}
}
