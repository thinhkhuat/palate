package distill

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

// fakeReplyServer always answers with content as the assistant message and
// hands the caller the raw request body of the most recent call.
type fakeReplyServer struct {
	content  string
	lastBody string
}

func newFakeReplyServer(t *testing.T, content string) (*httptest.Server, *fakeReplyServer) {
	t.Helper()
	f := &fakeReplyServer{content: content}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.lastBody = string(body)
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: f.content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return server, f
}

func gatewayFor(server *httptest.Server) *Gateway {
	return &Gateway{Endpoint: server.URL, Model: "test-model", HTTPClient: server.Client()}
}

func TestApplyDistilledDropsFlagged(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	statements := []DistilledStatement{
		{Statement: "Uses API key sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890 in scripts.", Category: "General", Confidence: 0.8, Evidence: "pasted a key"},
	}
	applied, dropped := ApplyDistilled(db, cfg, statements)
	if applied != 0 || dropped != 1 {
		t.Fatalf("applied=%d dropped=%d, want 0,1", applied, dropped)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("preferences count = %d, want 0 (flagged statement must never land, censored or not)", count)
	}
}

func TestApplyDistilledTrustTier(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	statements := []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.9, Evidence: "said so in chat"},
	}
	applied, dropped := ApplyDistilled(db, cfg, statements)
	if applied != 1 || dropped != 0 {
		t.Fatalf("applied=%d dropped=%d, want 1,0", applied, dropped)
	}

	var trustTier, source string
	err := db.QueryRow(`SELECT trust_tier, source FROM preferences WHERE statement=?`, "Prefers table-driven tests.").Scan(&trustTier, &source)
	if err != nil {
		t.Fatal(err)
	}
	if trustTier != "DISTILLED" {
		t.Fatalf("trust_tier = %q, want DISTILLED", trustTier)
	}
	if source != "llm_distilled" {
		t.Fatalf("source = %q, want llm_distilled", source)
	}
}

func TestApplyDistilledReviewMode(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	cfg.Distill.Mode = "review"

	statements := []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.9, Evidence: "said so in chat"},
	}
	applied, dropped := ApplyDistilled(db, cfg, statements)
	if applied != 1 || dropped != 0 {
		t.Fatalf("applied=%d dropped=%d, want 1,0", applied, dropped)
	}

	var prefCount, proposalCount int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&prefCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM proposals`).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if prefCount != 0 {
		t.Fatalf("preferences count = %d, want 0 (review mode must never call ApplySignal)", prefCount)
	}
	if proposalCount != 1 {
		t.Fatalf("proposals count = %d, want 1", proposalCount)
	}
}

func TestEvidenceRedactedBeforeSend(t *testing.T) {
	const secretKey = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890"
	newEvidence := "During the session the user pasted a key: " + secretKey + " into a test fixture."

	server, fake := newFakeReplyServer(t, `{"preferences": []}`)
	defer server.Close()

	db := testDB(t)
	_, err := Distill(context.Background(), db, gatewayFor(server), nil, "", newEvidence)
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}

	if strings.Contains(fake.lastBody, secretKey) {
		t.Fatalf("secret leaked into the HTTP request body: %s", fake.lastBody)
	}
	if !strings.Contains(fake.lastBody, "[redacted secret]") {
		t.Fatalf("expected the redaction marker in the request body, got: %s", fake.lastBody)
	}

	var drops int
	if err := db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='llm_evidence_secret'`).Scan(&drops); err != nil {
		t.Fatalf("capture_counters row for llm_evidence_secret missing: %v", err)
	}
	if drops != 1 {
		t.Fatalf("llm_evidence_secret drops = %d, want 1", drops)
	}
}

func TestApplyDistilledNeverStoresRawDiffLine(t *testing.T) {
	diffLine := `+	userID := ctx.Value("uid").(string)`
	reply, err := json.Marshal(struct {
		Preferences []DistilledStatement `json:"preferences"`
	}{Preferences: []DistilledStatement{
		{Statement: diffLine, Category: "General", Confidence: 0.8, Evidence: diffLine},
	}})
	if err != nil {
		t.Fatal(err)
	}

	server, _ := newFakeReplyServer(t, string(reply))
	defer server.Close()

	db := testDB(t)
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "", "diff:\n"+diffLine)
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("Distill returned %d statements, want 1: %v", len(result.Statements), result.Statements)
	}

	cfg := config.Default()
	applied, dropped := ApplyDistilled(db, cfg, result.Statements)
	if applied != 0 || dropped != 1 {
		t.Fatalf("applied=%d dropped=%d, want 0,1 (a statement identical to its own evidence line must be refused)", applied, dropped)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("preferences count = %d, want 0", count)
	}
}

func TestDistillRoundTripsEnvelope(t *testing.T) {
	reply := `{"preferences":[{"statement":"Prefers table-driven tests.","category":"Testing","confidence":0.9,"evidence":"said so"}]}`
	server, _ := newFakeReplyServer(t, reply)
	defer server.Close()

	db := testDB(t)
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "previously analyzed text", "new evidence text")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("Statements = %v, want 1 entry", result.Statements)
	}
	if result.Statements[0].Statement != "Prefers table-driven tests." {
		t.Fatalf("Statements[0] = %+v", result.Statements[0])
	}
	if result.RawReply != reply {
		t.Fatalf("RawReply = %q, want %q", result.RawReply, reply)
	}
}

func TestDistillFinalFailureBumpsParseCounter(t *testing.T) {
	server, _ := newFakeReplyServer(t, "not json, and the retry won't fix it either")
	defer server.Close()

	db := testDB(t)
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "", "new evidence")
	if err == nil {
		t.Fatalf("Distill: expected an error for unparseable output")
	}
	if result.RawReply == "" {
		t.Fatalf("RawReply must carry the last raw reply on failure")
	}

	var drops int
	if err := db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='llm_distilled_parse'`).Scan(&drops); err != nil {
		t.Fatalf("capture_counters row for llm_distilled_parse missing: %v", err)
	}
	if drops != 1 {
		t.Fatalf("llm_distilled_parse drops = %d, want 1", drops)
	}
}

// TestApplyDistilledJudgeSeam: with distill.Judge set, every clean statement
// goes through the seam (and none through ApplySignal); the safety drops still
// happen before the seam sees anything.
func TestApplyDistilledJudgeSeam(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	var seen []string
	Judge = func(_ context.Context, _ *sql.DB, _ config.Config, s DistilledStatement) error {
		seen = append(seen, s.Statement)
		return nil
	}
	t.Cleanup(func() { Judge = nil })

	statements := []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.7, Evidence: "said so"},
		{Statement: "Uses API key sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890 in scripts.", Category: "General", Confidence: 0.8, Evidence: "pasted a key"},
	}
	applied, dropped := ApplyDistilled(db, cfg, statements)
	if applied != 1 || dropped != 1 {
		t.Fatalf("applied=%d dropped=%d, want 1,1", applied, dropped)
	}
	if len(seen) != 1 || seen[0] != "Prefers table-driven tests." {
		t.Fatalf("seam saw %v, want exactly the clean statement", seen)
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&count)
	if count != 0 {
		t.Fatalf("the seam owns the write: ApplySignal must not have inserted, got %d rows", count)
	}
}
