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
	"github.com/thinhkhuat/palate/internal/learn"
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
	applied, dropped := ApplyDistilled(db, cfg, statements, "")
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
	applied, dropped := ApplyDistilled(db, cfg, statements, "")
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
	applied, dropped := ApplyDistilled(db, cfg, statements, "")
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
	_, err := Distill(context.Background(), db, gatewayFor(server), nil, "", newEvidence, "")
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
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "", "diff:\n"+diffLine, "")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("Distill returned %d statements, want 1: %v", len(result.Statements), result.Statements)
	}

	cfg := config.Default()
	applied, dropped := ApplyDistilled(db, cfg, result.Statements, "")
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
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "previously analyzed text", "new evidence text", "")
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
	result, err := Distill(context.Background(), db, gatewayFor(server), nil, "", "new evidence", "")
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

// TestApplyDistilledProjectFlag proves phase-03 req 3 end to end on the
// heuristic (no-Judge) path: a distilled statement flagged "project" carries
// learn.TagProjectSpecific in provenance and SelectProfile excludes it, even
// though the row itself is still stored as a candidate.
func TestApplyDistilledProjectFlag(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	stmt := "Uses a Makefile target named build-linux."
	applied, dropped := ApplyDistilled(db, cfg, []DistilledStatement{
		{Statement: stmt, Category: "Tooling", Confidence: 0.8, Evidence: "said so", Project: true},
	}, "")
	if applied != 1 || dropped != 0 {
		t.Fatalf("applied=%d dropped=%d, want 1,0", applied, dropped)
	}

	var id, prov string
	if err := db.QueryRow(`SELECT id, provenance FROM preferences WHERE statement=?`, stmt).Scan(&id, &prov); err != nil {
		t.Fatal(err)
	}
	if !learn.HasProjectSpecific(prov) {
		t.Fatalf("a project:true statement must carry the project_specific tag: %q", prov)
	}

	profile, err := learn.SelectProfile(db, cfg, cfg.TokenBudgets.PrewarmSession)
	if err != nil {
		t.Fatal(err)
	}
	if reason, excluded := profile.Excluded[id]; !excluded || reason != "project-specific" {
		t.Fatalf("a project-tagged row must be excluded from the profile, got excluded=%v reason=%q", excluded, reason)
	}
}

// TestApplyDistilledThreadsSession proves phase-03 req 1's ApplyDistilled
// half: an optional sessionKey rides the create event so
// learn.SessionCounts sees it, and an omitted key writes no session field at
// all (byte-for-byte legacy shape, backward compatible with the one
// remaining 3-argument call site outside this package).
func TestApplyDistilledThreadsSession(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()

	applied, _ := ApplyDistilled(db, cfg, []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.8, Evidence: "e"},
	}, "claude_code:abc123456789")
	if applied != 1 {
		t.Fatalf("applied=%d, want 1", applied)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE event_type='create'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"session":"claude_code:abc123456789"`) {
		t.Fatalf("create event must carry the session key: %s", payload)
	}

	applied, _ = ApplyDistilled(db, cfg, []DistilledStatement{
		{Statement: "Prefers tabs.", Category: "Formatting", Confidence: 0.8, Evidence: "e"},
	}, "")
	if applied != 1 {
		t.Fatalf("applied=%d, want 1", applied)
	}
	var tabsID string
	if err := db.QueryRow(`SELECT id FROM preferences WHERE statement='Prefers tabs.'`).Scan(&tabsID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT payload FROM events WHERE event_type='create' AND json_extract(payload,'$.id')=?`, tabsID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "session") {
		t.Fatalf("an omitted session key must write no session field: %s", payload)
	}
}

// TestAssistantOnlyEvidenceDropped covers phase-03 req 4's three cases:
// evidence found only in an assistant message is dropped and counted;
// evidence also found in a user message is kept; a paraphrase found in
// neither is kept, since origin cannot be told.
func TestAssistantOnlyEvidenceDropped(t *testing.T) {
	batch := []Message{
		{Role: "user", Content: "Please always use table-driven tests here."},
		{Role: "assistant", Content: "Understood — I switched this file to use golden-file tests for renderers."},
	}
	statements := []DistilledStatement{
		{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.8, Evidence: "always use table-driven tests"},
		{Statement: "Prefers golden-file tests for renderers.", Category: "Testing", Confidence: 0.8, Evidence: "switched this file to use golden-file tests for renderers"},
		{Statement: "Prefers small functions.", Category: "General", Confidence: 0.6, Evidence: "inferred preference for small functions"},
	}

	db := testDB(t)
	kept, dropped := filterAssistantOnlyEvidence(db, batch, statements)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	var keptStatements []string
	for _, s := range kept {
		keptStatements = append(keptStatements, s.Statement)
	}
	if len(kept) != 2 || keptStatements[0] != "Prefers table-driven tests." || keptStatements[1] != "Prefers small functions." {
		t.Fatalf("kept = %v, want the user-evidenced and the unattributable paraphrase, not the assistant-only one", keptStatements)
	}

	var drops int
	if err := db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='llm_distilled_assistant_evidence'`).Scan(&drops); err != nil {
		t.Fatalf("capture_counters row missing: %v", err)
	}
	if drops != 1 {
		t.Fatalf("llm_distilled_assistant_evidence drops = %d, want 1", drops)
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
	applied, dropped := ApplyDistilled(db, cfg, statements, "")
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
