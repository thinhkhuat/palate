package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/render"
	"github.com/thinhkhuat/palate/internal/storage"
)

// rowSpec describes one preference row inserted straight into the store, so a
// test controls tier, age and the sessions behind it exactly.
type rowSpec struct {
	id, stmt, tier      string
	conf                float64
	created, reinforced time.Time // zero → now
	sessions            []string  // one create/reinforce event each; nil → one session named after the id
}

func insertRow(t *testing.T, db *sql.DB, r rowSpec) {
	t.Helper()
	if r.tier == "" {
		r.tier = "DISTILLED"
	}
	if r.conf == 0 {
		r.conf = 0.5
	}
	now := time.Now().UTC()
	if r.created.IsZero() {
		r.created = now
	}
	if r.reinforced.IsZero() {
		r.reinforced = r.created
	}
	source, signal := "llm_distilled", "llm_distilled"
	if r.tier == "STATED" {
		source, signal = "explicit_prefer", "explicit_prefer"
	}
	prov, _ := json.Marshal([]string{signal})
	_, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, reinforcement_count, trust_tier, veracity)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.id, "Testing", r.stmt, r.conf, 0.5, source, string(prov), r.created.Format(time.RFC3339),
		r.created.Format(time.RFC3339), r.reinforced.Format(time.RFC3339), 0, r.tier, signal)
	if err != nil {
		t.Fatal(err)
	}
	sessions := r.sessions
	if sessions == nil {
		sessions = []string{"s-" + r.id}
	}
	for i, s := range sessions {
		typ := "create"
		if i > 0 {
			typ = "reinforce"
		}
		payload := fmt.Sprintf(`{"id":%q,"c":%f,"signal":%q,"session":%q}`, r.id, r.conf, signal, s)
		if err := storage.AppendEvent(db, typ, payload); err != nil {
			t.Fatal(err)
		}
	}
}

// stubReply is what the stub gateway returns for one call.
type stubReply struct {
	actions []Action
	sleep   time.Duration
}

// stubGateway is an httptest-backed gateway whose behaviour is decided per
// call (0-based), recording every user prompt it received.
type stubGateway struct {
	mu      sync.Mutex
	prompts []string
}

func (s *stubGateway) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

func newStub(t *testing.T, handle func(call int, prompt string) stubReply) (*distill.Gateway, *stubGateway) {
	t.Helper()
	st := &stubGateway{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				prompt = m.Content
			}
		}
		st.mu.Lock()
		call := len(st.prompts)
		st.prompts = append(st.prompts, prompt)
		st.mu.Unlock()
		rep := handle(call, prompt)
		if rep.sleep > 0 {
			select {
			case <-time.After(rep.sleep):
			case <-r.Context().Done():
				return
			}
		}
		body, _ := json.Marshal(map[string]interface{}{"actions": rep.actions})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(body)}}}})
	}))
	t.Cleanup(srv.Close)
	return &distill.Gateway{Endpoint: srv.URL, Model: "fake", HTTPClient: srv.Client(), Timeout: 5 * time.Second}, st
}

var promptIDRe = regexp.MustCompile(`(?m)^- id=(\S+) \|`)

// section returns the rows listed between <tag> and </tag> of a user prompt.
func section(prompt, tag string) []string {
	start := strings.Index(prompt, "<"+tag+">")
	end := strings.Index(prompt, "</"+tag+">")
	if start < 0 || end < start {
		return nil
	}
	var ids []string
	for _, m := range promptIDRe.FindAllStringSubmatch(prompt[start:end], -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func isLive(db *sql.DB, id string) bool {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&n)
	return n == 1
}

func confOf(db *sql.DB, id string) float64 {
	var c float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE id=?`, id).Scan(&c)
	return c
}

func rowCount(db *sql.DB, table string) int {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
	return n
}

// candidateRows inserts n distilled one-session rows c000…, confidence falling
// with the index so the candidate order (confidence desc) is the id order.
func candidateRows(t *testing.T, db *sql.DB, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%03d", i)
		insertRow(t, db, rowSpec{id: ids[i], stmt: fmt.Sprintf("candidate statement number %03d", i), conf: 0.9 - 0.001*float64(i)})
	}
	return ids
}

func statedRows(t *testing.T, db *sql.DB, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
		insertRow(t, db, rowSpec{id: ids[i], stmt: fmt.Sprintf("profile statement number %02d", i), tier: "STATED", conf: 0.95})
	}
	return ids
}

func retireActions(ids []string) []Action {
	out := make([]Action, len(ids))
	for i, id := range ids {
		out[i] = Action{Type: "retire", ID: id, Reason: "one-off"}
	}
	return out
}

func inProfile(t *testing.T, db *sql.DB, cfg config.Config, id string) bool {
	t.Helper()
	p, err := learn.SelectProfile(db, cfg, render.ProfileBudget(cfg))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range p.In {
		if row.ID == id {
			return true
		}
	}
	return false
}

func TestRunBatchesWholeLedger(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates int
		wantCalls  int
	}{
		{"130 candidates → 4 calls", 130, 4},
		{"230 candidates → 6 calls, none beyond the old 200 cut hidden", 230, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, cfg := testStore(t)
			profile := statedRows(t, db, 5)
			cand := candidateRows(t, db, tc.candidates)
			gw, st := newStub(t, func(int, string) stubReply { return stubReply{} })

			rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if st.calls() != tc.wantCalls || rep.Batches != tc.wantCalls || rep.Rows != tc.candidates {
				t.Fatalf("calls=%d batches=%d rows=%d, want %d calls/batches and %d rows", st.calls(), rep.Batches, rep.Rows, tc.wantCalls, tc.candidates)
			}
			seen := map[string]bool{}
			for i, prompt := range st.prompts {
				got := map[string]bool{}
				for _, id := range section(prompt, "profile") {
					got[id] = true
				}
				for _, id := range profile {
					if !got[id] {
						t.Fatalf("call %d: profile row %s missing from the request", i, id)
					}
				}
				batch := section(prompt, "candidates")
				if want := 40; i < tc.wantCalls-1 && len(batch) != want {
					t.Fatalf("call %d carried %d candidates, want %d", i, len(batch), want)
				}
				for _, id := range batch {
					seen[id] = true
				}
			}
			for _, id := range cand {
				if !seen[id] {
					t.Fatalf("candidate %s was never sent to the model", id)
				}
			}
		})
	}
}

func TestRunKeepsEarlierBatchesOnTimeout(t *testing.T) {
	db, cfg := testStore(t)
	cfg.Consolidate.EveryEvents = 1
	ids := candidateRows(t, db, 100) // 3 batches: 40, 40, 20
	gw, st := newStub(t, func(call int, _ string) stubReply {
		switch call {
		case 0:
			return stubReply{actions: []Action{{Type: "rescore", ID: ids[0], Confidence: 0.61}}}
		case 1:
			return stubReply{actions: []Action{{Type: "rescore", ID: ids[40], Confidence: 0.62}}}
		}
		return stubReply{sleep: 3 * time.Second}
	})
	gw.Timeout = 300 * time.Millisecond

	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("a timed-out batch must fail the pass with a gateway error, got %v", err)
	}
	if rep.Outcome != "error" || rep.Batches != 2 || st.calls() != 3 {
		t.Fatalf("outcome=%q batches=%d calls=%d, want error/2/3", rep.Outcome, rep.Batches, st.calls())
	}
	if confOf(db, ids[0]) != 0.61 || confOf(db, ids[40]) != 0.62 {
		t.Fatalf("batches 1 and 2 must stay applied: %v %v", confOf(db, ids[0]), confOf(db, ids[40]))
	}
	if n := rowCount(db, "consolidations"); n != 0 {
		t.Fatalf("a failed pass must not write a consolidations row, got %d", n)
	}
	if _, _, due := Due(db, cfg); !due {
		t.Fatal("Due must stay true after a failed pass so the next session retries")
	}
}

func TestGuardsNarrowed(t *testing.T) {
	hourAgo := time.Now().UTC().Add(-time.Hour)
	run := func(t *testing.T, db *sql.DB, cfg config.Config, opts RunOptions, actions []Action) Report {
		t.Helper()
		gw, _ := newStub(t, func(int, string) stubReply { return stubReply{actions: actions} })
		rep, err := Run(context.Background(), db, cfg, gw, opts)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	t.Run("one session reinforced an hour ago is retirable", func(t *testing.T) {
		db, cfg := testStore(t)
		statedRows(t, db, 9)
		insertRow(t, db, rowSpec{id: "one", stmt: "a one-off remark", reinforced: hourAgo})
		rep := run(t, db, cfg, RunOptions{}, retireActions([]string{"one"}))
		if rep.Applied != 1 || rep.Skipped != 0 || isLive(db, "one") {
			t.Fatalf("report %+v live=%v, want the row retired", rep, isLive(db, "one"))
		}
	})

	t.Run("two sessions reinforced an hour ago is shielded", func(t *testing.T) {
		db, cfg := testStore(t)
		statedRows(t, db, 9)
		insertRow(t, db, rowSpec{id: "two", stmt: "a repeated preference", sessions: []string{"s1", "s2"}, reinforced: hourAgo})
		candidateRows(t, db, 1) // a candidate, so the pass calls the model
		rep := run(t, db, cfg, RunOptions{}, retireActions([]string{"two"}))
		if rep.Applied != 0 || rep.Skipped != 1 || !isLive(db, "two") {
			t.Fatalf("report %+v live=%v, want the row shielded", rep, isLive(db, "two"))
		}
	})

	t.Run("share over 40% of the ledger becomes proposals, exactly 40% applies", func(t *testing.T) {
		db, cfg := testStore(t)
		ids := candidateRows(t, db, 10)
		rep := run(t, db, cfg, RunOptions{}, retireActions(ids[:5]))
		if rep.Mode != "review(guard)" || rep.Applied != 0 || rep.Proposed != 5 || liveCount(db) != 10 {
			t.Fatalf("5 of 10 must be proposals only: %+v live=%d", rep, liveCount(db))
		}
		db2, cfg2 := testStore(t)
		ids2 := candidateRows(t, db2, 10)
		rep2 := run(t, db2, cfg2, RunOptions{}, retireActions(ids2[:4]))
		if rep2.Mode != "auto" || rep2.Applied != 4 || rep2.Proposed != 0 || liveCount(db2) != 6 {
			t.Fatalf("4 of 10 is within the guard: %+v live=%d", rep2, liveCount(db2))
		}
	})

	t.Run("rebuild sees every row and skips the 40% guard, a normal run does not", func(t *testing.T) {
		db, cfg := testStore(t)
		ids := candidateRows(t, db, 10)
		// A far-future applied watermark: nothing counts as changed since it.
		if _, err := db.Exec(`INSERT INTO consolidations(last_event_id, actions, mode, at) VALUES(0,0,'auto','2099-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		gw, st := newStub(t, func(int, string) stubReply { return stubReply{actions: retireActions(ids[:6])} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil || rep.Rows != 0 || st.calls() != 0 || rep.Outcome != "nothing-to-do" {
			t.Fatalf("a normal run sees only the delta: %+v calls=%d err=%v", rep, st.calls(), err)
		}
		rep, err = Run(context.Background(), db, cfg, gw, RunOptions{Rebuild: true})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Rows != 10 || rep.Applied != 6 || rep.Mode != "auto" || liveCount(db) != 4 {
			t.Fatalf("rebuild must see all 10 rows and apply 6 unguarded: %+v live=%d", rep, liveCount(db))
		}
	})
}

func TestRewriteSafetyInPass(t *testing.T) {
	db, cfg := testStore(t)
	insertRow(t, db, rowSpec{id: "A", stmt: "Never commits to main directly", tier: "STATED", conf: 0.95})
	insertRow(t, db, rowSpec{id: "B", stmt: "Thích viết test trước khi viết code"})
	insertRow(t, db, rowSpec{id: "C", stmt: "Prefers tests"})
	insertRow(t, db, rowSpec{id: "D", stmt: "Always runs the whole linter pass before committing"})
	insertRow(t, db, rowSpec{id: "E1", stmt: "Likes short functions"})
	insertRow(t, db, rowSpec{id: "E2", stmt: "Keeps functions small", tier: "STATED", conf: 0.95})
	for i := 0; i < 30; i++ {
		insertRow(t, db, rowSpec{id: fmt.Sprintf("f%02d", i), stmt: fmt.Sprintf("filler statement %02d", i)})
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{
			{Type: "rewrite", ID: "A", Statement: "Never commits to main.", Reason: "tighten"},
			{Type: "rewrite", ID: "B", Statement: "Prefers tests first", Reason: "translate"},
			{Type: "rewrite", ID: "C", Statement: "Prefers tests written before the implementation", Reason: "expand"},
			{Type: "rewrite", ID: "D", Statement: "Runs the linter before committing.", Reason: "tighten"},
			{Type: "rewrite", ID: "D", Statement: "Lints before commit.", Reason: "tighten again"},
			{Type: "merge", IDs: []string{"E1", "E2"}, Statement: "Short functions", Reason: "same"},
		}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 2 || rep.Skipped != 4 || rep.Mode != "auto" {
		t.Fatalf("want 2 applied (D once, the merge) and 4 refused: %+v", rep)
	}
	stmt := func(id string) string {
		var s string
		_ = db.QueryRow(`SELECT statement FROM preferences WHERE id=?`, id).Scan(&s)
		return s
	}
	for id, want := range map[string]string{
		"A": "Never commits to main directly",
		"B": "Thích viết test trước khi viết code",
		"C": "Prefers tests",
		"D": "Runs the linter before committing.",
	} {
		if got := stmt(id); got != want {
			t.Fatalf("row %s = %q, want %q", id, got, want)
		}
	}
	if got := stmt("E2"); got != "Keeps functions small" || !isLive(db, "E2") || isLive(db, "E1") {
		t.Fatalf("the merge must keep the STATED survivor's text and close the other: %q live(E2)=%v live(E1)=%v", got, isLive(db, "E2"), isLive(db, "E1"))
	}
	var drops int
	_ = db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='consolidate_rewrite_refused'`).Scan(&drops)
	if drops != 4 {
		t.Fatalf("consolidate_rewrite_refused = %d, want 4", drops)
	}
}

func TestRetireResolvesConflict(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 8)
	old := time.Now().UTC().Add(-48 * time.Hour)
	insertRow(t, db, rowSpec{id: "stale", stmt: "Prefers tabs", conf: 0.8, sessions: []string{"s1", "s2"}, created: old})
	insertRow(t, db, rowSpec{id: "fresh", stmt: "Prefers spaces", conf: 0.5, sessions: []string{"s3", "s4"}, created: old})
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES('cf1','stale','fresh','tabs vs spaces')`); err != nil {
		t.Fatal(err)
	}
	if !inProfile(t, db, cfg, "stale") || inProfile(t, db, cfg, "fresh") {
		t.Fatal("before the pass the higher-confidence side renders and the loser is hidden")
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "retire", ID: "stale", Reason: "stale side of a conflict"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil || rep.Applied != 1 {
		t.Fatalf("retire not applied: %+v err=%v", rep, err)
	}
	var resolved sql.NullString
	_ = db.QueryRow(`SELECT resolved_at FROM conflicts WHERE id='cf1'`).Scan(&resolved)
	if !resolved.Valid || resolved.String == "" {
		t.Fatal("retiring a side must resolve the conflict")
	}
	if !inProfile(t, db, cfg, "fresh") || isLive(db, "stale") {
		t.Fatalf("the surviving side must now render: in profile=%v stale live=%v", inProfile(t, db, cfg, "fresh"), isLive(db, "stale"))
	}
}

func TestMergeUnionsSessions(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 8)
	insertRow(t, db, rowSpec{id: "X", stmt: "Writes tests before code always", sessions: []string{"sess-a"}})
	insertRow(t, db, rowSpec{id: "Y", stmt: "Test first, then code", sessions: []string{"sess-b"}})
	counts, _ := learn.SessionCounts(db)
	if counts["X"] != 1 || counts["Y"] != 1 || inProfile(t, db, cfg, "X") {
		t.Fatalf("before: X=%d Y=%d inProfile=%v, want 1, 1, false", counts["X"], counts["Y"], inProfile(t, db, cfg, "X"))
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "merge", IDs: []string{"X", "Y"}, Statement: "Writes tests first.", Reason: "same"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil || rep.Applied != 1 {
		t.Fatalf("merge not applied: %+v err=%v", rep, err)
	}
	counts, _ = learn.SessionCounts(db)
	if counts["X"] != 2 || !isLive(db, "X") || isLive(db, "Y") {
		t.Fatalf("after: X sessions=%d live(X)=%v live(Y)=%v, want 2, true, false", counts["X"], isLive(db, "X"), isLive(db, "Y"))
	}
	if !inProfile(t, db, cfg, "X") {
		t.Fatal("the merged row, now backed by 2 sessions, must be eligible for the profile")
	}
}

func TestProjectAction(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	old := time.Now().UTC().Add(-48 * time.Hour)
	insertRow(t, db, rowSpec{id: "P", stmt: "Builds with the Makefile target build-linux", conf: 0.6, sessions: []string{"s1", "s2"}, created: old})
	if !inProfile(t, db, cfg, "P") {
		t.Fatal("setup: P must start in the profile")
	}
	candidateRows(t, db, 1) // a candidate, so the pass calls the model
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "project", ID: "P", Reason: "repo-specific make target"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil || rep.Applied != 1 {
		t.Fatalf("project action not applied: %+v err=%v", rep, err)
	}
	var prov, payload string
	_ = db.QueryRow(`SELECT provenance FROM preferences WHERE id='P'`).Scan(&prov)
	_ = db.QueryRow(`SELECT payload FROM events WHERE event_type='consolidate_project'`).Scan(&payload)
	if !strings.Contains(prov, `"project_specific"`) || payload != `{"id":"P","reason":"repo-specific make target"}` {
		t.Fatalf("tag/event wrong: provenance=%s payload=%s", prov, payload)
	}
	p, err := learn.SelectProfile(db, cfg, render.ProfileBudget(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if p.Excluded["P"] != "project-specific" {
		t.Fatalf("P must leave the profile as project-specific, got %q", p.Excluded["P"])
	}
}

func TestStaleCandidateForget(t *testing.T) {
	now := time.Now().UTC()
	days := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	seedStale := func(t *testing.T, db *sql.DB) {
		insertRow(t, db, rowSpec{id: "old", stmt: "a remark from a month ago", created: days(31)})
		insertRow(t, db, rowSpec{id: "young", stmt: "a remark from last month", created: days(29)})
		insertRow(t, db, rowSpec{id: "stated", stmt: "an old stated preference", tier: "STATED", conf: 0.95, created: days(31)})
		insertRow(t, db, rowSpec{id: "member", stmt: "an old repeated preference", sessions: []string{"s1", "s2"}, created: days(31)})
		insertRow(t, db, rowSpec{id: "recent", stmt: "an old row seen again today", created: days(31), reinforced: now.Add(-time.Hour)})
	}
	wantReason := fmt.Sprintf("stale_candidate: 1 session, created %s, not repeated within 30 days", days(31).Format("2006-01-02"))

	t.Run("after the batches: only the 31-day one-session distilled row goes, and restore reopens it", func(t *testing.T) {
		db, cfg := testStore(t)
		seedStale(t, db)
		gw, st := newStub(t, func(int, string) stubReply { return stubReply{} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if st.calls() != 1 || rep.Retired != 1 || rep.Outcome != "applied" {
			t.Fatalf("calls=%d %+v, want 1 call and exactly 1 retired", st.calls(), rep)
		}
		for id, want := range map[string]bool{"old": false, "young": true, "stated": true, "member": true, "recent": true} {
			if isLive(db, id) != want {
				t.Fatalf("live(%s)=%v, want %v", id, isLive(db, id), want)
			}
		}
		var payload string
		_ = db.QueryRow(`SELECT payload FROM events WHERE event_type='consolidate_retire'`).Scan(&payload)
		if want := fmt.Sprintf(`{"id":"old","reason":%q}`, wantReason); payload != want {
			t.Fatalf("event payload\n got %s\nwant %s", payload, want)
		}
		if found, err := learn.Restore(db, "old"); err != nil || !found || !isLive(db, "old") {
			t.Fatalf("restore must reopen the row: found=%v err=%v live=%v", found, err, isLive(db, "old"))
		}
	})

	t.Run("nothing changed: no gateway call, the code step still runs", func(t *testing.T) {
		db, cfg := testStore(t)
		seedStale(t, db)
		if _, err := db.Exec(`INSERT INTO consolidations(last_event_id, actions, mode, at) VALUES(0,0,'auto','2099-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		gw, st := newStub(t, func(int, string) stubReply { return stubReply{} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil || st.calls() != 0 || rep.Retired != 1 || rep.Outcome != "applied" || isLive(db, "old") {
			t.Fatalf("calls=%d %+v err=%v live(old)=%v", st.calls(), rep, err, isLive(db, "old"))
		}
	})

	t.Run("stale_candidate_days 0 turns it off", func(t *testing.T) {
		db, cfg := testStore(t)
		cfg.Consolidate.StaleCandidateDays = 0
		seedStale(t, db)
		gw, _ := newStub(t, func(int, string) stubReply { return stubReply{} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil || rep.Retired != 0 || !isLive(db, "old") {
			t.Fatalf("%+v err=%v live(old)=%v, want nothing retired", rep, err, isLive(db, "old"))
		}
	})

	t.Run("review mode never retires", func(t *testing.T) {
		db, cfg := testStore(t)
		cfg.Distill.Mode = "review"
		seedStale(t, db)
		gw, _ := newStub(t, func(int, string) stubReply { return stubReply{} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil || rep.Retired != 0 || !isLive(db, "old") {
			t.Fatalf("%+v err=%v live(old)=%v, want nothing retired", rep, err, isLive(db, "old"))
		}
	})
}

func TestGuardedBatchDoesNotStallDelta(t *testing.T) {
	db, cfg := testStore(t)
	ids := candidateRows(t, db, 100) // batches: c000-c039, c040-c079, c080-c099
	gw, st := newStub(t, func(call int, _ string) stubReply {
		switch call {
		case 0:
			return stubReply{actions: retireActions(ids[0:5])}
		case 1:
			return stubReply{actions: retireActions(ids[40:76])} // 5 + 36 = 41 of 100 → over 40%
		}
		return stubReply{actions: retireActions(ids[80:85])}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.calls() != 3 || rep.Applied != 10 || rep.Proposed != 36 || rep.Mode != "auto" || rep.Outcome != "applied" {
		t.Fatalf("calls=%d %+v, want 3 calls, 10 applied, 36 proposed, mode auto", st.calls(), rep)
	}
	if liveCount(db) != 90 || !isLive(db, ids[40]) || isLive(db, ids[0]) || isLive(db, ids[80]) {
		t.Fatalf("batches 1 and 3 apply, batch 2 stays live: live=%d", liveCount(db))
	}

	// The next pass re-sends only the guarded batch's rows (named by pending
	// proposals), not the whole ledger.
	gw2, st2 := newStub(t, func(int, string) stubReply { return stubReply{} })
	rep2, err := Run(context.Background(), db, cfg, gw2, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sent := section(st2.prompts[0], "candidates")
	if rep2.Rows != 36 || len(sent) != 36 || st2.calls() != 1 {
		t.Fatalf("second pass rows=%d sent=%d calls=%d, want 36/36/1", rep2.Rows, len(sent), st2.calls())
	}
	want := map[string]bool{}
	for _, id := range ids[40:76] {
		want[id] = true
	}
	for _, id := range sent {
		if !want[id] {
			t.Fatalf("second pass sent %s, which was not part of the guarded batch", id)
		}
	}
}

func TestGuardDenominatorIsLedger(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, config.Config, []string) {
		db, cfg := testStore(t)
		statedRows(t, db, 20)
		return db, cfg, candidateRows(t, db, 380) // ledger 400, profile 20
	}

	t.Run("25 retires in one batch of 40 is 25/400, not guarded", func(t *testing.T) {
		db, cfg, ids := setup(t)
		gw, _ := newStub(t, func(call int, _ string) stubReply {
			if call == 0 {
				return stubReply{actions: retireActions(ids[:25])}
			}
			return stubReply{}
		})
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Applied != 25 || rep.Proposed != 0 || rep.Mode != "auto" || liveCount(db) != 375 {
			t.Fatalf("%+v live=%d, want 25 applied and 375 live", rep, liveCount(db))
		}
	})

	t.Run("17 per batch: 153 cumulative applies, the batch reaching 170 of 400 is guarded", func(t *testing.T) {
		db, cfg, ids := setup(t)
		gw, st := newStub(t, func(call int, _ string) stubReply {
			return stubReply{actions: retireActions(ids[40*call : 40*call+17])}
		})
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if st.calls() != 10 || rep.Applied != 153 || rep.Proposed != 17 || rep.Mode != "auto" || liveCount(db) != 247 {
			t.Fatalf("calls=%d %+v live=%d, want 10 calls, 153 applied, 17 proposed, 247 live", st.calls(), rep, liveCount(db))
		}
		if !isLive(db, ids[360]) {
			t.Fatal("the guarded batch's rows must stay live")
		}
	})
}

func TestPromptCarriesBudget(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 3)
	candidateRows(t, db, 2)
	budget := render.ProfileBudget(cfg)
	sel, err := learn.SelectProfile(db, cfg, budget)
	if err != nil {
		t.Fatal(err)
	}
	gw, st := newStub(t, func(int, string) stubReply { return stubReply{} })
	if _, err := Run(context.Background(), db, cfg, gw, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("target: %d tokens (estimated). current profile size: %d tokens.", budget, sel.Tokens)
	if sel.Tokens == 0 || !strings.Contains(st.prompts[0], want) {
		t.Fatalf("request must carry %q (profile tokens %d):\n%s", want, sel.Tokens, st.prompts[0])
	}
	if !strings.Contains(SystemPrompt, "<budget>") {
		t.Fatal("the system prompt must explain the <budget> block")
	}
}

func TestProposalNotDuplicated(t *testing.T) {
	db, cfg := testStore(t)
	ids := candidateRows(t, db, 5)
	actions := []Action{
		{Type: "rescore", ID: ids[0], Confidence: 0.7},
		{Type: "rescore", ID: ids[1], Confidence: 0.7},
		{Type: "rescore", ID: ids[2], Confidence: 0.7},
	}
	gw, _ := newStub(t, func(int, string) stubReply { return stubReply{actions: actions} })
	rep1, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep1.Proposed != 3 || rep1.Mode != "review(guard)" {
		t.Fatalf("first pass: %+v, want 3 proposals in review(guard)", rep1)
	}
	rep2, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var pending int
	_ = db.QueryRow(`SELECT count(*) FROM proposals WHERE source='consolidate' AND status='pending'`).Scan(&pending)
	if rep2.Proposed != 0 || pending != 3 {
		t.Fatalf("second pass proposed %d, pending=%d, want 0 new and 3 pending", rep2.Proposed, pending)
	}
}

func TestConsolidateTimeoutKey(t *testing.T) {
	t.Setenv("PALATE_LLM_TIMEOUT", "")
	home := t.TempDir()
	path := home + "/config.yaml"
	if err := os.WriteFile(path, []byte("consolidate:\n  timeout_seconds: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = home
	cfg.LLM.Endpoint, cfg.LLM.Model = "http://gw.invalid/v1/chat/completions", "fake"
	gw, ok := BuildGateway(cfg)
	if !ok {
		t.Fatal("gateway not built")
	}
	if gw.Timeout != 7*time.Second {
		t.Fatalf("consolidate gateway timeout = %v, want 7s", gw.Timeout)
	}
	if cfg.LLMTimeout() != 30*time.Second {
		t.Fatalf("LLMTimeout() = %v, want the distill and judge paths to stay at 30s", cfg.LLMTimeout())
	}
	cfg.Consolidate.TimeoutSeconds = 0
	gw, _ = BuildGateway(cfg)
	if gw.Timeout != 300*time.Second {
		t.Fatalf("unset key = %v, want the 300s default", gw.Timeout)
	}
}

// TestConsolidateDefaultTimeout pins the owner default: 300 s on the
// consolidate gateway (client and struct) while distill/judge keep LLMTimeout.
func TestConsolidateDefaultTimeout(t *testing.T) {
	t.Setenv("PALATE_LLM_TIMEOUT", "")
	cfg := config.Default()
	cfg.Home = t.TempDir()
	cfg.LLM.Endpoint, cfg.LLM.Model = "http://gw.invalid/v1/chat/completions", "fake"
	if cfg.Consolidate.TimeoutSeconds != 300 {
		t.Fatalf("default consolidate.timeout_seconds = %d, want 300", cfg.Consolidate.TimeoutSeconds)
	}
	gw, ok := BuildGateway(cfg)
	if !ok {
		t.Fatal("gateway not built")
	}
	if gw.Timeout != 300*time.Second {
		t.Fatalf("consolidate gateway timeout = %v, want 300s", gw.Timeout)
	}
	if cfg.LLMTimeout() != 30*time.Second {
		t.Fatalf("LLMTimeout() = %v, want 30s unchanged", cfg.LLMTimeout())
	}
}
