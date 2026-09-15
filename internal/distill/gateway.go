// Package distill holds the only code path in Palate allowed to call an
// LLM: a stdlib HTTP client for an OpenAI-compatible chat-completions
// endpoint, configured exclusively from environment variables.
package distill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

// requestTimeout is the fallback per-request timeout when a Gateway is built
// without going through NewGatewayFromEnv (tests, direct construction). The
// live value comes from cfg.LLMTimeout() so a mature whole-profile
// consolidation pass is not cut off mid-generation.
const requestTimeout = 30 * time.Second

// Gateway is a client for one OpenAI-compatible /v1/chat/completions
// endpoint.
type Gateway struct {
	Endpoint   string
	Model      string
	APIKey     string
	HTTPClient *http.Client
	// Timeout bounds one request. A zero value means requestTimeout. It is
	// carried on the struct (not only on HTTPClient) because doRequest also
	// derives a context deadline from it, and that deadline is what actually
	// fires first on a long generation.
	Timeout time.Duration

	// statePath, when set, receives a last_gateway.json stamp after every
	// successful call (doctor's staleness surface). Set only by
	// NewGatewayFromEnv; directly-built gateways (tests) don't stamp.
	statePath string
}

// requestTimeoutOrDefault returns the gateway's per-request timeout.
func (g *Gateway) requestTimeoutOrDefault() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return requestTimeout
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

var httpWarningOnce sync.Once

// NewGatewayFromEnv builds a Gateway from cfg.LLM. It returns (nil, false)
// when the config does not carry enough to attempt a call. When the
// endpoint uses http:// to a non-loopback host, it prints one warning to
// stderr (once per process) and still returns a usable Gateway — the
// endpoint is the caller's own infrastructure, so this is advisory, not a
// refusal.
func NewGatewayFromEnv(cfg config.Config) (*Gateway, bool) {
	if !cfg.LLMConfigured() {
		return nil, false
	}
	warnIfPlaintext(cfg.LLM.Endpoint)
	return &Gateway{
		Endpoint:   cfg.LLM.Endpoint,
		Model:      cfg.LLM.Model,
		APIKey:     cfg.LLM.APIKey,
		HTTPClient: &http.Client{Timeout: cfg.LLMTimeout()},
		Timeout:    cfg.LLMTimeout(),
		statePath:  cfg.StatePath("last_gateway.json"),
	}, true
}

func warnIfPlaintext(endpoint string) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" {
		return
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return
	}
	httpWarningOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "palate: warning: PALATE_LLM_ENDPOINT uses http:// to a non-local host — evidence and any API key travel in plaintext")
	})
}

// Complete sends systemPrompt and userPrompt as the opening messages of a
// chat-completion request and returns the assistant's raw text content.
//
// A transport error, a non-200 response, or an empty choices array fails
// immediately with no retry. When validate is non-nil and rejects a
// successful reply, Complete appends the rejected reply and a corrective
// user message to the conversation and retries, up to retries additional
// attempts. On final failure it returns the last raw content alongside the
// validation error, so the caller can log it.
func (g *Gateway) Complete(ctx context.Context, systemPrompt, userPrompt string, retries int, validate func(raw string) error) (string, error) {
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: g.requestTimeoutOrDefault()}
	}

	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var lastContent string
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		content, err := g.doRequest(ctx, client, messages)
		if err != nil {
			return "", err
		}
		lastContent = content

		if validate == nil {
			return content, nil
		}
		if err := validate(content); err == nil {
			return content, nil
		} else {
			lastErr = err
		}

		if attempt == retries {
			break
		}
		messages = append(messages,
			chatMessage{Role: "assistant", Content: content},
			chatMessage{Role: "user", Content: correctiveMessage(content)},
		)
	}
	return lastContent, lastErr
}

func correctiveMessage(raw string) string {
	snippet := raw
	if len(snippet) > 400 {
		snippet = snippet[:400]
	}
	return fmt.Sprintf("Your previous reply was not valid JSON: %s. Reply with only the JSON object.", snippet)
}

func (g *Gateway) doRequest(ctx context.Context, client *http.Client, messages []chatMessage) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, g.requestTimeoutOrDefault())
	defer cancel()

	body, err := json.Marshal(chatRequest{
		Model:       g.Model,
		Messages:    messages,
		Temperature: 0.2,
	})
	if err != nil {
		return "", fmt.Errorf("gateway: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, g.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gateway: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if g.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gateway: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway: status %d: %s", resp.StatusCode, truncate(string(respBody), 400))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("gateway: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("gateway: empty choices in response")
	}

	g.stampSuccess()
	return parsed.Choices[0].Message.Content, nil
}

// gatewayStamp is what state/last_gateway.json records after every
// successful gateway response: the model and endpoint the call actually
// used. Doctor compares it with the current env so a stale process env
// (harness started before harness-env.sh changed) becomes visible instead
// of silently deciding what the judge sees.
type gatewayStamp struct {
	Model        string `json:"model"`
	EndpointHost string `json:"endpoint_host"`
	At           string `json:"at"`
}

// stampSuccess records the last successful gateway call. Best-effort: a
// stamp write must never fail a completed call.
func (g *Gateway) stampSuccess() {
	if g.statePath == "" {
		return
	}
	host := ""
	if u, err := url.Parse(g.Endpoint); err == nil {
		host = u.Host
	}
	b, err := json.Marshal(gatewayStamp{Model: g.Model, EndpointHost: host, At: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(g.statePath), 0700)
	_ = os.WriteFile(g.statePath, b, 0600)
}

// LastGatewayStamp returns the model and timestamp of the last successful
// gateway call, ok=false when there is none (or the stamp is unreadable).
func LastGatewayStamp(cfg config.Config) (model, at string, ok bool) {
	b, err := os.ReadFile(cfg.StatePath("last_gateway.json"))
	if err != nil {
		return "", "", false
	}
	var s gatewayStamp
	if json.Unmarshal(b, &s) != nil || s.Model == "" {
		return "", "", false
	}
	return s.Model, s.At, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
