package mcp

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

// safeBuf is a bytes.Buffer the server goroutine and the test may share.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// runStdio serves the given stdin bytes through the guarded transport with a
// stdin that closes right after the last byte — the exact shape of
// `printf req | palate serve` — and returns stdout plus Run's error.
func runStdio(t *testing.T, stdin string, grace time.Duration) (string, error) {
	t.Helper()
	memStore(t)
	out := &safeBuf{}
	tr := &stdioTransport{in: strings.NewReader(stdin), out: out, grace: grace}
	srv := NewServer(config.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := srv.Run(ctx, tr)
	return out.String(), err
}

func TestStdioDrainsRequestBeforeEOF(t *testing.T) {
	for i := 0; i < 5; i++ { // the SDK race was 5/5 reproducible before the guard
		out, err := runStdio(t, initReq+"\n", stdioDrainGrace)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !strings.Contains(out, `"serverInfo"`) {
			t.Fatalf("run %d: initialize reply lost; stdout=%q", i, out)
		}
	}
}

func TestStdioAnswersParseErrorAndKeepsServing(t *testing.T) {
	out, err := runStdio(t, "not valid json {{{\n"+initReq+"\n", stdioDrainGrace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"code":-32700`) || !strings.Contains(lines[1], `"serverInfo"`) {
		t.Fatalf("want -32700 line then initialize reply, got %q", out)
	}
}

func TestStdioEmptyStdinExitsClean(t *testing.T) {
	out, err := runStdio(t, "", stdioDrainGrace)
	if err != nil || out != "" {
		t.Fatalf("want clean exit with no output, got err=%v out=%q", err, out)
	}
}

func TestStdioBatchAndNotificationCounting(t *testing.T) {
	cases := []struct {
		msg  string
		req  int
		resp int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 1, 0},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, 0, 0},
		{`{"jsonrpc":"2.0","id":"a","result":{}}`, 0, 1},
		{`{"jsonrpc":"2.0","id":null,"error":{"code":-32700}}`, 0, 0},
		{`[{"jsonrpc":"2.0","id":1,"method":"x"},{"jsonrpc":"2.0","method":"n"},{"jsonrpc":"2.0","id":2,"method":"y"}]`, 2, 0},
		{`[{"jsonrpc":"2.0","id":1,"result":1},{"jsonrpc":"2.0","id":2,"error":{}}]`, 0, 2},
		{`"just a string"`, 0, 0},
	}
	for _, c := range cases {
		if got := countRequests([]byte(c.msg)); got != c.req {
			t.Errorf("countRequests(%s) = %d, want %d", c.msg, got, c.req)
		}
		if got := countResponses([]byte(c.msg)); got != c.resp {
			t.Errorf("countResponses(%s) = %d, want %d", c.msg, got, c.resp)
		}
	}
}

// A request whose reply never comes must not hold the process forever: after
// the grace period the guard releases EOF to the SDK.
func TestStdioGraceTimeoutReleasesEOF(t *testing.T) {
	g := &stdioGuard{out: io.Discard, grace: 50 * time.Millisecond, drained: make(chan struct{})}
	pr, pw := io.Pipe()
	start := time.Now()
	go g.pump(strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"never/answered"}`+"\n"), pw)
	data, err := io.ReadAll(pr) // returns once pump closes the pipe
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"never/answered"`) {
		t.Fatalf("request not forwarded: %q", data)
	}
	if el := time.Since(start); el < 50*time.Millisecond || el > 5*time.Second {
		t.Fatalf("EOF released after %v, want ≈ grace", el)
	}
}
<<<<<<< Updated upstream
=======

// slowWriter models a host that has stopped draining stdout.
type slowWriter struct{ block chan struct{} }

func (w slowWriter) Write(p []byte) (int, error) { <-w.block; return len(p), nil }

// A stalled stdout consumer must not stall stdin ingestion: the ledger lock
// is released before the real write, so add() returns while Write blocks.
func TestStdioStalledOutputDoesNotBlockIngestion(t *testing.T) {
	w := slowWriter{block: make(chan struct{})}
	g := &stdioGuard{out: w, grace: time.Second, drained: make(chan struct{})}
	g.add(1)
	started := make(chan struct{})
	go func() {
		close(started)
		g.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // let Write reach the blocking out.Write
	done := make(chan struct{})
	go func() { g.add(1); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("add() blocked behind a stalled stdout write")
	}
	close(w.block)
}
>>>>>>> Stashed changes
