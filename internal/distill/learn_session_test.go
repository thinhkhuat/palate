package distill

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

// countingGateway wraps an httptest server that always replies with the
// same outcome, counting how many requests it actually received — the
// assertion that run 2 makes zero gateway calls depends on this, not just
// on the output.
type countingGateway struct {
	mu    sync.Mutex
	calls int
}

// newCountingSuccessServer replies 200 with assistantContent wrapped as
// the chat-completion envelope's assistant message (what Gateway.Complete
// actually unwraps before ParseResponse ever sees assistantContent).
func newCountingSuccessServer(t *testing.T, cg *countingGateway, assistantContent string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cg.mu.Lock()
		cg.calls++
		cg.mu.Unlock()
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: assistantContent}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newCountingFailureServer replies with a non-200 status, counting calls —
// Gateway.Complete fails on the status check before ever parsing the body.
func newCountingFailureServer(t *testing.T, cg *countingGateway, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cg.mu.Lock()
		cg.calls++
		cg.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func (cg *countingGateway) count() int {
	cg.mu.Lock()
	defer cg.mu.Unlock()
	return cg.calls
}

func learnSessionFixtureTranscript(t *testing.T) Transcript {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_code_session.jsonl")
	copyFixture(t, "claude_code_session.jsonl", path)
	return Transcript{Harness: "claude_code", Path: path}
}

func TestLearnSessionFixtureTwice(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	reply, err := json.Marshal(struct {
		Preferences []DistilledStatement `json:"preferences"`
	}{Preferences: []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.9, Evidence: "always use table-driven tests"},
		{Statement: "Prefers any over interface{}.", Category: "Types", Confidence: 0.85, Evidence: "don't use interface{}, use any instead"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	cg := &countingGateway{}
	server := newCountingSuccessServer(t, cg, string(reply))
	defer server.Close()
	gw := gatewayFor(server)

	reader := claudeCodeReader{}
	transcript := learnSessionFixtureTranscript(t)

	summary1, err := LearnSession(context.Background(), db, cfg, gw, reader, transcript)
	if err != nil {
		t.Fatalf("run 1: LearnSession: %v", err)
	}
	if summary1.Applied < 2 {
		t.Fatalf("run 1: Applied = %d, want >= 2", summary1.Applied)
	}
	if !summary1.Advanced {
		t.Fatalf("run 1: Advanced = false, want true")
	}
	if cg.count() != 1 {
		t.Fatalf("run 1: gateway calls = %d, want 1", cg.count())
	}

	var confidences []float64
	rows, err := db.Query(`SELECT confidence FROM preferences ORDER BY statement`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c float64
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		confidences = append(confidences, c)
	}
	rows.Close()
	if len(confidences) < 2 {
		t.Fatalf("preferences stored = %d, want >= 2", len(confidences))
	}

	var prefCountAfterRun1 int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&prefCountAfterRun1); err != nil {
		t.Fatal(err)
	}

	summary2, err := LearnSession(context.Background(), db, cfg, gw, reader, transcript)
	if err != nil {
		t.Fatalf("run 2: LearnSession: %v", err)
	}
	if !summary2.Skipped {
		t.Fatalf("run 2: Skipped = false, want true (cursor already covers every message)")
	}
	if cg.count() != 1 {
		t.Fatalf("run 2: gateway calls = %d, want still 1 (no new messages, window empty, no request made)", cg.count())
	}

	var prefCountAfterRun2 int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&prefCountAfterRun2); err != nil {
		t.Fatal(err)
	}
	if prefCountAfterRun2 != prefCountAfterRun1 {
		t.Fatalf("run 2 added %d rows, want 0 new rows", prefCountAfterRun2-prefCountAfterRun1)
	}

	var confidencesAfter []float64
	rows, err = db.Query(`SELECT confidence FROM preferences ORDER BY statement`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c float64
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		confidencesAfter = append(confidencesAfter, c)
	}
	rows.Close()
	if len(confidencesAfter) != len(confidences) {
		t.Fatalf("confidence row count changed: %d -> %d", len(confidences), len(confidencesAfter))
	}
	for i := range confidences {
		if confidences[i] != confidencesAfter[i] {
			t.Fatalf("confidence[%d] changed from %v to %v on a no-op run", i, confidences[i], confidencesAfter[i])
		}
	}
}

func TestLearnSessionCursorOnFailure(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	cg := &countingGateway{}
	server := newCountingFailureServer(t, cg, http.StatusInternalServerError, "server exploded")
	defer server.Close()
	gw := gatewayFor(server)

	reader := claudeCodeReader{}
	transcript := learnSessionFixtureTranscript(t)

	for i := 1; i <= 2; i++ {
		summary, err := LearnSession(context.Background(), db, cfg, gw, reader, transcript)
		if err != nil {
			t.Fatalf("attempt %d: LearnSession: %v", i, err)
		}
		if summary.Advanced {
			t.Fatalf("attempt %d: Advanced = true, want false (gateway failed)", i)
		}
		if summary.Reason == "" {
			t.Fatalf("attempt %d: Reason is empty, want the gateway error", i)
		}
		_, attempts, ok := GetCursor(db, "claude_code", transcript.Path)
		if !ok || attempts != i {
			t.Fatalf("attempt %d: cursor attempts = %d (ok=%v), want %d", i, attempts, ok, i)
		}
		pos, _, _ := GetCursor(db, "claude_code", transcript.Path)
		if pos != "" {
			t.Fatalf("attempt %d: cursor position = %q, want unmoved (empty)", i, pos)
		}
	}

	// Third consecutive failure: cursor force-advances and the window is
	// counted as abandoned.
	summary, err := LearnSession(context.Background(), db, cfg, gw, reader, transcript)
	if err != nil {
		t.Fatalf("attempt 3: LearnSession: %v", err)
	}
	if !summary.Advanced || !summary.Abandoned {
		t.Fatalf("attempt 3: Advanced=%v Abandoned=%v, want both true after 3 failures", summary.Advanced, summary.Abandoned)
	}
	pos, attempts, ok := GetCursor(db, "claude_code", transcript.Path)
	if !ok || pos == "" {
		t.Fatalf("attempt 3: cursor position = %q (ok=%v), want a non-empty forced position", pos, ok)
	}
	if attempts != 0 {
		t.Fatalf("attempt 3: attempts = %d, want 0 (SetCursor resets the streak)", attempts)
	}

	var drops int
	if err := db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='llm_distilled_window_abandoned'`).Scan(&drops); err != nil {
		t.Fatalf("capture_counters row for llm_distilled_window_abandoned missing: %v", err)
	}
	if drops != 1 {
		t.Fatalf("llm_distilled_window_abandoned drops = %d, want 1", drops)
	}

	if cg.count() != 3 {
		t.Fatalf("gateway calls = %d, want 3 (one per attempt)", cg.count())
	}
}

// stubReader serves an in-memory transcript so a backlog of arbitrary length
// can be walked without a fixture file.
type stubReader struct{ msgs []Message }

func (s stubReader) Harness() string                        { return "claude_code" }
func (s stubReader) Discover(string) ([]Transcript, error)  { return nil, nil }
func (s stubReader) Messages(Transcript) ([]Message, error) { return s.msgs, nil }

func backlog(n int) []Message {
	out := make([]Message, n)
	for i := range out {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out[i] = Message{Role: role, Content: "turn " + strconv.Itoa(i), Index: i}
	}
	return out
}

// A first run over a long backlog walks it in batches of batchSize, one
// gateway call each, and leaves the cursor at the last message; a second run
// then makes no call at all.
func TestLearnSessionBatchesBacklog(t *testing.T) {
	conn := testDB(t)
	cg := &countingGateway{}
	srv := newCountingSuccessServer(t, cg, `{"preferences":[]}`)
	defer srv.Close()
	gw := &Gateway{Endpoint: srv.URL, Model: "m"}
	cfg := config.Default()

	reader := stubReader{msgs: backlog(45)}
	tr := Transcript{Harness: "claude_code", Path: filepath.Join(t.TempDir(), "backlog.jsonl")}
	sum, err := LearnSession(context.Background(), conn, cfg, gw, reader, tr)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if sum.Batches != 3 || cg.count() != 3 {
		t.Fatalf("run 1: batches=%d calls=%d, want 3 and 3 (45 messages / 20 per batch)", sum.Batches, cg.count())
	}
	if pos, _, ok := GetCursor(conn, "claude_code", tr.Path); !ok || pos != "44" {
		t.Fatalf("run 1: cursor = %q ok=%v, want 44", pos, ok)
	}
	sum2, err := LearnSession(context.Background(), conn, cfg, gw, reader, tr)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !sum2.Skipped || cg.count() != 3 {
		t.Fatalf("run 2: skipped=%v calls=%d, want skipped and still 3", sum2.Skipped, cg.count())
	}
}

// A failure mid-backlog keeps the cursor at the last successful batch so the
// next run resumes there instead of re-learning or skipping it.
func TestLearnSessionBatchFailureKeepsCursor(t *testing.T) {
	conn := testDB(t)
	cg := &countingGateway{}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: `{"preferences":[]}`}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	_ = cg
	gw := &Gateway{Endpoint: srv.URL, Model: "m"}
	reader := stubReader{msgs: backlog(45)}
	tr := Transcript{Harness: "claude_code", Path: filepath.Join(t.TempDir(), "backlog.jsonl")}
	sum, err := LearnSession(context.Background(), conn, config.Default(), gw, reader, tr)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.Batches != 2 || sum.Reason == "" {
		t.Fatalf("batches=%d reason=%q, want 2 batches and a failure reason", sum.Batches, sum.Reason)
	}
	if pos, attempts, ok := GetCursor(conn, "claude_code", tr.Path); !ok || pos != "19" || attempts != 1 {
		t.Fatalf("cursor = %q attempts=%d ok=%v, want 19 / 1", pos, attempts, ok)
	}
}
