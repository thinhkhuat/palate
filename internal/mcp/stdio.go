package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioDrainGrace bounds how long a closed stdin waits for in-flight replies
// before the SDK is allowed to see EOF. A handler that is still running after
// this (a slow gateway judge, say) is abandoned exactly as the SDK would have.
const stdioDrainGrace = 30 * time.Second

// stdioTransport is the SDK stdio transport with two guarantees go-sdk v1.7.0
// does not give:
//
//  1. a request that arrived before stdin closed still gets its reply — the
//     SDK cancels every in-flight handler the moment its reader returns EOF
//     (internal/jsonrpc2/conn.go, readIncoming), so `echo req | palate serve`
//     lost the reply and exited 1;
//  2. a non-JSON line is answered with a JSON-RPC -32700 and skipped — the SDK
//     decoder treats it as a fatal stream error and ends the session.
//
// Requests read are counted against responses written; on EOF the reader
// holds the SDK's EOF until that count is zero (or stdioDrainGrace passes).
// Real clients keep stdin open for the whole session and never notice this.
type stdioTransport struct {
	in    io.Reader
	out   io.Writer
	grace time.Duration
}

func newStdioTransport() *stdioTransport {
	return &stdioTransport{in: os.Stdin, out: os.Stdout, grace: stdioDrainGrace}
}

func (t *stdioTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	g := &stdioGuard{out: t.out, grace: t.grace, drained: make(chan struct{})}
	pr, pw := io.Pipe()
	go g.pump(t.in, pw)
	return (&mcp.IOTransport{Reader: pr, Writer: g}).Connect(ctx)
}

// stdioGuard owns the request/response ledger and serialises every write to
// the real output — the SDK's replies and our own -32700 lines share it.
type stdioGuard struct {
	out   io.Writer
	grace time.Duration

<<<<<<< Updated upstream
	mu       sync.Mutex
=======
	// writeMu serialises the SDK's replies against the pump's -32700 lines on
	// the shared output. It is never held together with mu, so a stalled stdout
	// consumer cannot stall stdin ingestion.
	writeMu sync.Mutex

	mu       sync.Mutex    // guards the ledger below only
>>>>>>> Stashed changes
	pending  int           // requests read minus responses written
	draining bool          // stdin hit EOF; waiting for pending to reach zero
	drained  chan struct{} // closed once draining && pending == 0
}

// pump copies stdin line by line into the SDK's reader, answering garbage
// inline and deferring EOF while replies are outstanding.
func (g *stdioGuard) pump(in io.Reader, pw *io.PipeWriter) {
	r := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if json.Valid(trimmed) {
				g.add(countRequests(trimmed))
				if _, werr := pw.Write(append(trimmed, '\n')); werr != nil {
					return // SDK side closed; nothing left to deliver
				}
			} else {
				g.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}` + "\n"))
			}
		}
		if err != nil {
			g.beginDrain()
			select {
			case <-g.drained:
			case <-time.After(g.grace):
			}
			pw.CloseWithError(io.EOF)
			return
		}
	}
}

// Write receives every message the SDK emits (one message or one batch per
// call, newline-terminated) and settles the ledger before forwarding it.
func (g *stdioGuard) Write(p []byte) (int, error) {
	g.mu.Lock()
<<<<<<< Updated upstream
	defer g.mu.Unlock()
=======
>>>>>>> Stashed changes
	if n := countResponses(bytes.TrimSpace(p)); n > 0 {
		g.pending -= n
		if g.pending < 0 {
			g.pending = 0
		}
		g.signalLocked()
	}
<<<<<<< Updated upstream
=======
	g.mu.Unlock()

	g.writeMu.Lock()
	defer g.writeMu.Unlock()
>>>>>>> Stashed changes
	return g.out.Write(p)
}

func (g *stdioGuard) Close() error { return nil }

func (g *stdioGuard) add(n int) {
	if n == 0 {
		return
	}
	g.mu.Lock()
	g.pending += n
	g.mu.Unlock()
}

func (g *stdioGuard) beginDrain() {
	g.mu.Lock()
	g.draining = true
	g.signalLocked()
	g.mu.Unlock()
}

// signalLocked closes drained exactly once, when nothing is outstanding.
func (g *stdioGuard) signalLocked() {
	if g.draining && g.pending == 0 {
		select {
		case <-g.drained:
		default:
			close(g.drained)
		}
	}
}

// rpcHead is the part of a JSON-RPC message the ledger cares about.
type rpcHead struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func (h rpcHead) hasID() bool {
	return len(h.ID) > 0 && !bytes.Equal(h.ID, []byte("null"))
}

// heads decodes one message or one batch into its heads; anything else is empty.
func heads(msg []byte) []rpcHead {
	if len(msg) > 0 && msg[0] == '[' {
		var batch []rpcHead
		if json.Unmarshal(msg, &batch) == nil {
			return batch
		}
		return nil
	}
	var one rpcHead
	if json.Unmarshal(msg, &one) == nil {
		return []rpcHead{one}
	}
	return nil
}

// countRequests counts client messages that expect a reply: id + method.
// Notifications (no id) and client responses (no method) expect nothing.
func countRequests(msg []byte) int {
	n := 0
	for _, h := range heads(msg) {
		if h.hasID() && h.Method != "" {
			n++
		}
	}
	return n
}

// countResponses counts server messages that settle a request: id, no method.
func countResponses(msg []byte) int {
	n := 0
	for _, h := range heads(msg) {
		if h.hasID() && h.Method == "" {
			n++
		}
	}
	return n
}
