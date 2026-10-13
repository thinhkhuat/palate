// Package distill holds the only code path in Palate allowed to call an
// LLM: a stdlib HTTP client for an OpenAI-compatible chat-completions
// endpoint, configured exclusively from environment variables.
package distill

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

// requestTimeout is the fallback per-request timeout when a Gateway is built
// without going through NewGatewayFromEnv (tests, direct construction). The
// live value comes from cfg.LLMTimeout() so a mature whole-profile
// consolidation pass is not cut off mid-generation.
const requestTimeout = 30 * time.Second

// streamTotalCapFactor sets the backstop on one streamed call: the per-request
// timeout times this factor (300 s -> 20 min for the dream pass). The request
// timeout itself only counts connecting, response headers and the silence
// between two stream reads, so a healthy long generation is not cut by it.
const streamTotalCapFactor = 4

// Gateway is a client for one OpenAI-compatible /v1/chat/completions
// endpoint.
type Gateway struct {
	Endpoint   string
	Model      string
	APIKey     string
	HTTPClient *http.Client
	// Timeout is the per-request limit. A zero value means requestTimeout. For
	// a streamed reply it bounds connecting plus response headers and then the
	// silence between two reads; a non-stream JSON reply gets it as one
	// deadline. doRequest enforces it with its own timers and strips
	// HTTPClient.Timeout (which would count the whole body read) from the
	// client it uses.
	Timeout time.Duration

	// statePath, when set, receives a last_gateway.json stamp after every
	// successful call (doctor's staleness surface). Set only by
	// NewGatewayFromEnv; directly-built gateways (tests) don't stamp.
	statePath string

	// totalCap, when set, overrides the default total cap on one streamed call.
	totalCap time.Duration
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
	// Stream asks for SSE so bytes flow while the model reasons; a proxy such
	// as Cloudflare cuts a connection that stays silent for ~100-125 s.
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamChunk is one SSE `data:` payload. Only content deltas are kept;
// reasoning_content / reasoning deltas are ignored.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
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
		HTTPClient: &http.Client{},
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
		client = &http.Client{}
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

// limitError is the cancellation cause recorded when one of doRequest's own
// limits fires, so the error names which limit it was.
type limitError string

func (e limitError) Error() string { return string(e) }

// idleReader runs touch after every read that returned bytes, so each received
// line (keep-alive comments and blank lines included) resets the idle timer.
type idleReader struct {
	r     io.Reader
	touch func()
}

func (i idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.touch()
	}
	return n, err
}

func (g *Gateway) totalCapOrDefault() time.Duration {
	if g.totalCap > 0 {
		return g.totalCap
	}
	return streamTotalCapFactor * g.requestTimeoutOrDefault()
}

func (g *Gateway) doRequest(ctx context.Context, client *http.Client, messages []chatMessage) (string, error) {
	timeout := g.requestTimeoutOrDefault()
	total := g.totalCapOrDefault()
	start := time.Now()

	capCtx, capCancel := context.WithTimeoutCause(ctx, total,
		limitError(fmt.Sprintf("stream exceeded the total cap of %s (%dx the request timeout)", total, streamTotalCapFactor)))
	defer capCancel()
	reqCtx, cancel := context.WithCancelCause(capCtx)
	defer cancel(nil)

	// client.Timeout would count the whole body read; the timers below replace it.
	c := *client
	c.Timeout = 0
	client = &c

	// limitHit returns the error for the limit that fired, or nil when none of
	// ours did (a caller cancel or an ordinary failure keeps its own wording).
	limitHit := func() error {
		var le limitError
		if errors.As(context.Cause(reqCtx), &le) {
			return fmt.Errorf("gateway: %w", le)
		}
		return nil
	}

	limit := time.AfterFunc(timeout, func() {
		cancel(limitError(fmt.Sprintf("no response headers within the request timeout of %s", timeout)))
	})
	defer func() { limit.Stop() }()

	body, err := json.Marshal(chatRequest{
		Model:         g.Model,
		Messages:      messages,
		Temperature:   0.2,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
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
		if e := limitHit(); e != nil {
			return "", e
		}
		return "", fmt.Errorf("gateway: request failed: %w", err)
	}
	defer resp.Body.Close()
	limit.Stop()

	if resp.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		idle := time.AfterFunc(timeout, func() {
			cancel(limitError(fmt.Sprintf("stream idle: no data for %s (the request timeout)", timeout)))
		})
		limit = idle
		content, err := readSSE(idleReader{r: resp.Body, touch: func() { idle.Reset(timeout) }})
		if err != nil {
			if e := limitHit(); e != nil {
				return "", e
			}
			return "", err
		}
		g.stampSuccess()
		return content, nil
	}

	// Non-stream reply: one deadline for the whole request, as before.
	limit = time.AfterFunc(max(timeout-time.Since(start), 0), func() {
		cancel(limitError(fmt.Sprintf("reply not complete within the request timeout of %s", timeout)))
	})

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if e := limitHit(); e != nil {
			return "", e
		}
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

// readSSE assembles the assistant text from a chat-completions SSE stream:
// it concatenates choices[0].delta.content. The stream is complete at
// `data: [DONE]`, or at EOF when a choice already carried a finish_reason
// (the owner's gateway ends its stream that way, without [DONE]). A stream
// that ends with neither, carries an error event, stops on finish_reason
// "length" or "content_filter", or yields no content is an error; a partial answer is never
// returned as success.
func readSSE(body io.Reader) (string, error) {
	r := bufio.NewReader(body)
	var out strings.Builder
	finished := false
	done := func() (string, error) {
		if out.Len() == 0 {
			return "", fmt.Errorf("gateway: stream ended with no content")
		}
		return out.String(), nil
	}
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data := strings.TrimSpace(rest)
			if data == "[DONE]" {
				return done()
			}
			var chunk streamChunk
			if jerr := json.Unmarshal([]byte(data), &chunk); jerr != nil {
				return "", fmt.Errorf("gateway: decode stream event: %w", jerr)
			}
			if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
				return "", fmt.Errorf("gateway: stream error: %s", truncate(streamErrorMessage(chunk.Error), 400))
			}
			if len(chunk.Choices) > 0 {
				out.WriteString(chunk.Choices[0].Delta.Content)
				switch chunk.Choices[0].FinishReason {
				case "":
				case "length":
					return "", fmt.Errorf("gateway: stream stopped at the length limit (finish_reason length)")
				case "content_filter":
					return "", fmt.Errorf("gateway: stream stopped by the content filter (finish_reason content_filter)")
				default:
					finished = true
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				if finished {
					return done()
				}
				return "", fmt.Errorf("gateway: stream ended before [DONE]")
			}
			return "", fmt.Errorf("gateway: read stream: %w", err)
		}
	}
}

// streamErrorMessage returns error.message when the event carries one, else
// the raw error value.
func streamErrorMessage(raw json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	return string(raw)
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
