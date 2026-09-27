package distill

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
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

// TestLearnSessionRecordsSession covers phase-03 req 1's LearnSession half:
// the same transcript path reinforced across two separate runs is one
// session (the key is derived from the path, not the call), while a
// different transcript path reinforcing the same statement counts as a
// second, distinct session.
func TestLearnSessionRecordsSession(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	reply, err := json.Marshal(struct {
		Preferences []DistilledStatement `json:"preferences"`
	}{Preferences: []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.9, Evidence: "always use table-driven tests"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cg := &countingGateway{}
	server := newCountingSuccessServer(t, cg, string(reply))
	defer server.Close()
	gw := gatewayFor(server)

	tr1 := Transcript{Harness: "claude_code", Path: filepath.Join(t.TempDir(), "a.jsonl")}
	reader1 := stubReader{msgs: []Message{{Role: "user", Content: "always use table-driven tests", Index: 0}}}
	if _, err := LearnSession(context.Background(), db, cfg, gw, reader1, tr1); err != nil {
		t.Fatalf("run 1: %v", err)
	}

	// Same transcript path, a second call (a later hook fire on the same
	// session): the window grew, so this batch is genuinely new, but the
	// session key — derived from tr1.Path — is unchanged.
	reader1Grown := stubReader{msgs: []Message{
		{Role: "user", Content: "always use table-driven tests", Index: 0},
		{Role: "user", Content: "please always use table-driven tests here too", Index: 1},
	}}
	if _, err := LearnSession(context.Background(), db, cfg, gw, reader1Grown, tr1); err != nil {
		t.Fatalf("run 1 (grown): %v", err)
	}

	var id string
	if err := db.QueryRow(`SELECT id FROM preferences WHERE statement='Prefers table-driven tests.'`).Scan(&id); err != nil {
		t.Fatalf("row not found: %v", err)
	}
	counts, err := learn.SessionCounts(db)
	if err != nil {
		t.Fatal(err)
	}
	if counts[id] != 1 {
		t.Fatalf("the same transcript path reinforced twice must count as 1 session, got %d", counts[id])
	}

	tr2 := Transcript{Harness: "claude_code", Path: filepath.Join(t.TempDir(), "b.jsonl")}
	reader2 := stubReader{msgs: []Message{{Role: "user", Content: "always use table-driven tests", Index: 0}}}
	if _, err := LearnSession(context.Background(), db, cfg, gw, reader2, tr2); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	counts, err = learn.SessionCounts(db)
	if err != nil {
		t.Fatal(err)
	}
	if counts[id] != 2 {
		t.Fatalf("a different transcript path must count as a second, distinct session, got %d", counts[id])
	}
}

// TestDistillerSeesCompareSetNotLedger covers phase-03 req 2: with 300 live
// rows, the distiller's CURRENT PREFERENCES section holds at most the
// profile plus compareTopK(20) bullets, never all 300 — the prompt is bounded
// by the compare set, not by the whole ledger (review F2).
func TestDistillerSeesCompareSetNotLedger(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	for i := 0; i < 300; i++ {
		sig := learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers option number " + strconv.Itoa(i) + " for widget config.", Category: "General"}
		if _, err := learn.ApplySignal(db, cfg, sig, nil); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	profile, err := learn.SelectProfile(db, cfg, cfg.TokenBudgets.PrewarmSession)
	if err != nil {
		t.Fatal(err)
	}

	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastBody = req.Messages[len(req.Messages)-1].Content
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: `{"preferences":[]}`}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	gw := gatewayFor(server)

	reader := stubReader{msgs: []Message{{Role: "user", Content: "totally unrelated new evidence text", Index: 0}}}
	tr := Transcript{Harness: "claude_code", Path: filepath.Join(t.TempDir(), "many.jsonl")}
	if _, err := LearnSession(context.Background(), db, cfg, gw, reader, tr); err != nil {
		t.Fatalf("LearnSession: %v", err)
	}

	start := strings.Index(lastBody, "## CURRENT PREFERENCES")
	end := strings.Index(lastBody, "## PREVIOUSLY ANALYZED")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("CURRENT PREFERENCES section not found: %s", lastBody)
	}
	bullets := strings.Count(lastBody[start:end], "\n- [")
	const compareTopK = 20 // phase-03 req 2's constant, unexported in package learn
	if bullets > len(profile.In)+compareTopK {
		t.Fatalf("CURRENT PREFERENCES held %d bullets, want <= profile(%d) + compareTopK(%d)", bullets, len(profile.In), compareTopK)
	}
}
