package distill

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const streamTestChunk = `data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n"

// streamStub serves an SSE reply through body, which gets a flush function and
// the request context. It never outlives the test: the handler also stops when
// done closes, so server.Close cannot hang on a stalled handler.
func streamStub(t *testing.T, body func(w http.ResponseWriter, flush func(), ctx context.Context)) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		body(w, func() { w.(http.Flusher).Flush() }, ctx)
	}))
	t.Cleanup(func() { close(done); server.Close() })
	return server
}

func streamGateway(t *testing.T, server *httptest.Server, timeout, totalCap time.Duration) *Gateway {
	t.Helper()
	g := newTestGateway(t, server, "")
	// Mirror NewGatewayFromEnv: the client carries the same Timeout as the
	// gateway, which must not cut a healthy stream either.
	g.HTTPClient = &http.Client{Timeout: timeout}
	g.Timeout = timeout
	g.totalCap = totalCap
	return g
}

func TestStreamOutlivesRequestTimeoutWhileBytesFlow(t *testing.T) {
	server := streamStub(t, func(w http.ResponseWriter, flush func(), ctx context.Context) {
		w.WriteHeader(http.StatusOK)
		flush()
		for i := 0; i < 12; i++ { // ~600 ms, three times the request timeout
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, streamTestChunk)
			flush()
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"!"},"finish_reason":"stop"}]}`+"\n\n")
		flush()
	})
	g := streamGateway(t, server, 200*time.Millisecond, 5*time.Second)
	got, err := g.Complete(context.Background(), "s", "u", 0, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if want := strings.Repeat("x", 12) + "!"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestStreamIdleGapTimesOut(t *testing.T) {
	server := streamStub(t, func(w http.ResponseWriter, flush func(), ctx context.Context) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, streamTestChunk)
		flush()
		<-ctx.Done() // stall
	})
	g := streamGateway(t, server, 200*time.Millisecond, 5*time.Second)
	start := time.Now()
	got, err := g.Complete(context.Background(), "s", "u", 0, nil)
	if err == nil {
		t.Fatalf("want an idle-gap error, got content %q", got)
	}
	if got != "" {
		t.Fatalf("partial content returned with the error: %q", got)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("error does not name the idle gap: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("idle gap took %v to fire", el)
	}
}

func TestStreamTotalCapStillApplies(t *testing.T) {
	server := streamStub(t, func(w http.ResponseWriter, flush func(), ctx context.Context) {
		w.WriteHeader(http.StatusOK)
		for ctx.Err() == nil {
			fmt.Fprint(w, streamTestChunk)
			flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	g := streamGateway(t, server, 200*time.Millisecond, 500*time.Millisecond)
	start := time.Now()
	got, err := g.Complete(context.Background(), "s", "u", 0, nil)
	if err == nil {
		t.Fatalf("want a total-cap error, got content %q", got)
	}
	if got != "" {
		t.Fatalf("partial content returned with the error: %q", got)
	}
	if !strings.Contains(err.Error(), "total cap") {
		t.Fatalf("error does not name the total cap: %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("total cap took %v to fire", el)
	}
}

func TestHeadersNeverArriveTimesOut(t *testing.T) {
	server := streamStub(t, func(w http.ResponseWriter, flush func(), ctx context.Context) {
		<-ctx.Done() // never write headers
	})
	g := streamGateway(t, server, 200*time.Millisecond, 5*time.Second)
	start := time.Now()
	_, err := g.Complete(context.Background(), "s", "u", 0, nil)
	el := time.Since(start)
	if err == nil {
		t.Fatal("want a headers timeout, got success")
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Fatalf("error does not name the headers limit: %v", err)
	}
	if el < 150*time.Millisecond || el > 1500*time.Millisecond {
		t.Fatalf("headers timeout fired after %v, want about 200ms", el)
	}
}

func TestJSONFallbackKeepsSingleDeadline(t *testing.T) {
	server := streamStub(t, func(w http.ResponseWriter, flush func(), ctx context.Context) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"choices":[`)
		flush()
		<-ctx.Done() // body never completes
	})
	g := streamGateway(t, server, 200*time.Millisecond, 5*time.Second)
	_, err := g.Complete(context.Background(), "s", "u", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "request timeout") {
		t.Fatalf("want the single-deadline error, got %v", err)
	}
}
