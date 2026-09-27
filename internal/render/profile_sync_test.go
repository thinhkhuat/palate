package render

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// seedStatedID inserts a live STATED row at an explicit id, for tests that
// need to name specific ids (conflicts, merges).
func seedStatedID(t *testing.T, db *sql.DB, id, category, statement string, confidence float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES(?,?,?,?,?,?)`,
		id, category, statement, confidence, "STATED", "explicit_prefer"); err != nil {
		t.Fatal(err)
	}
}

func seedDistilledID(t *testing.T, db *sql.DB, id, category, statement string, confidence float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES(?,?,?,?,?,?)`,
		id, category, statement, confidence, "DISTILLED", "llm_distilled"); err != nil {
		t.Fatal(err)
	}
}

func membershipEventFor(t *testing.T, db *sql.DB, eventType, id string) (found bool, payload string) {
	t.Helper()
	rows, err := db.Query(`SELECT payload FROM events WHERE event_type=? ORDER BY id`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(p), &v)
		if v.ID == id {
			found, payload = true, p
		}
	}
	return found, payload
}

// TestBodyIsBudgetedProfile proves criterion 1 (render level): TASTE.md's
// body is exactly what Body/SelectProfile returns — no separate ranking cut
// downstream — and the whole rendered file stays within the shared budget
// even with far more eligible rows than fit.
func TestBodyIsBudgetedProfile(t *testing.T) {
	db, cfg := newHomeCfg(t)
	for i := 0; i < 200; i++ {
		seedStated(t, db, cfg, "General", fmt.Sprintf("Prefers eligible row number %d with enough padding text to cost real tokens", i), 0.5)
	}
	body1, n1, err := Body(db, cfg, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body2, n2, err := Body(db, cfg, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if body1 != body2 || n1 != n2 {
		t.Fatal("Body must be a pure, deterministic function of the store")
	}
	if n1 == 0 || n1 >= 200 {
		t.Fatalf("expected the budget to admit some but not all 200 rows, got n=%d", n1)
	}

	full, err := Render(db, cfg, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if extractBody(full) != body1 {
		t.Fatal("TASTE.md's body must equal Body's own output byte-for-byte")
	}
	sessionMiddle := strings.TrimSuffix(strings.TrimPrefix(SessionHeader(cfg, n1)+body1+SessionTail(), SessionHeader(cfg, n1)), SessionTail())
	if sessionMiddle != body1 {
		t.Fatal("the SessionStart block must carry Body's bytes unmodified between its header and tail")
	}
	if learn.EstimateTokens(full) > cfg.TokenBudgets.PrewarmSession {
		t.Fatalf("TASTE.md over budget: %d > %d", learn.EstimateTokens(full), cfg.TokenBudgets.PrewarmSession)
	}
	if strings.Contains(body1, "more preferences not shown") {
		t.Fatal("the retired per-consumer fitting message must never appear — selection alone decides what shows")
	}
}

// TestBodyShowsConfidenceWinner is the regression test for the stale-side
// render bug: the old Body query excluded conflicts.conflicting_id
// unconditionally, so a higher-confidence row stored on that side of the
// row was hidden while its lower-confidence opponent showed. Here the
// higher-confidence row IS the conflicting_id.
func TestBodyShowsConfidenceWinner(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStatedID(t, db, "w", "Testing", "Prefers table-driven tests", 0.9)
	seedStatedID(t, db, "l", "Testing", "Prefers one assertion per test never table-driven", 0.3)
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES('c1','l','w','contradiction')`); err != nil {
		t.Fatal(err)
	}
	body, _, err := Body(db, cfg, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Prefers table-driven tests") {
		t.Fatalf("the higher-confidence side (stored as conflicting_id) must show:\n%s", body)
	}
	if strings.Contains(body, "one assertion per test") {
		t.Fatalf("the lower-confidence side must stay hidden:\n%s", body)
	}
}

// TestSyncEmitsProfileEvents covers requirement 3.2/3.3's whole diff-and-
// events contract.
func TestSyncEmitsProfileEvents(t *testing.T) {
	t.Run("first run seeds membership from disk and cuts with seed:true", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		// Already on disk when the first Sync ever runs: no profile_add for
		// it, since seeding treats it as an existing member, not new news.
		seedStatedID(t, db, "keep", "General", "Prefers keeping", 0.8)
		// A distilled, one-session row: an old binary would have shown it
		// (everything live rendered); under the new eligibility rule it is
		// excluded for lack of a second session, so it should seed-cut, not
		// silently vanish with no record at all.
		seedDistilledID(t, db, "legacy", "General", "Prefers legacy row", 0.5)
		// Genuinely new: absent from the pre-existing disk file, so it must
		// get a real profile_add even on the very first sync.
		seedStatedID(t, db, "fresh", "General", "Prefers a brand new row", 0.9)
		writeTaste(t, cfg, "# Taste\n\n## General\n\n- Prefers keeping. Confidence: 0.80\n- Prefers legacy row. Confidence: 0.50\n")

		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if found, _ := membershipEventFor(t, db, "profile_add", "keep"); found {
			t.Fatal("a row already on the seeded disk file must not get a fresh profile_add")
		}
		if found, payload := membershipEventFor(t, db, "profile_add", "fresh"); !found || !strings.Contains(payload, `"reason"`) {
			t.Fatalf("expected a profile_add for the genuinely new row with a reason, found=%v payload=%q", found, payload)
		}
		found, payload := membershipEventFor(t, db, "profile_cut", "legacy")
		if !found {
			t.Fatal("expected a profile_cut for the excluded legacy row")
		}
		if !strings.Contains(payload, `"seed":true`) {
			t.Fatalf("a first-run seed cut must carry seed:true: %s", payload)
		}
		if !strings.Contains(payload, "session") {
			t.Fatalf("expected the session-count exclusion reason, got %s", payload)
		}
	})

	t.Run("a retire carries the retiring event's own reason", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStatedID(t, db, "gone", "General", "Prefers vanishing", 0.7)
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if found, _ := membershipEventFor(t, db, "profile_add", "gone"); !found {
			t.Fatal("expected the row to enter the profile first")
		}
		if err := storage.Forget(db, "gone"); err != nil {
			t.Fatal(err)
		}
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		found, payload := membershipEventFor(t, db, "profile_cut", "gone")
		if !found {
			t.Fatal("expected a profile_cut once the row retired")
		}
		if !strings.Contains(payload, "owner forget") {
			t.Fatalf("the cut reason must come from the forget event, got %s", payload)
		}
	})

	t.Run("a merge names the survivor as superseded_by", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStatedID(t, db, "survivor", "General", "Prefers surviving", 0.8)
		seedStatedID(t, db, "folded", "General", "Prefers folding in", 0.6)
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE id='folded'`, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE preferences SET provenance='["stated_preference","merged_from:[\"folded\"]"]' WHERE id='survivor'`); err != nil {
			t.Fatal(err)
		}
		if err := storage.AppendEvent(db, "consolidate_merge", `{"id":"folded","into":"survivor","reason":"same taste"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		found, payload := membershipEventFor(t, db, "profile_cut", "folded")
		if !found {
			t.Fatal("expected a profile_cut for the merged-away row")
		}
		if !strings.Contains(payload, `"superseded_by":"survivor"`) {
			t.Fatalf("expected superseded_by=survivor, got %s", payload)
		}
	})

	t.Run("an unchanged DB writes no new profile events and does not rewrite the file", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStatedID(t, db, "steady", "General", "Prefers staying put", 0.7)
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		var before int
		_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type IN ('profile_add','profile_cut')`).Scan(&before)
		wrote, err := Sync(db, cfg, cfg.TastePath())
		if err != nil {
			t.Fatal(err)
		}
		if wrote {
			t.Fatal("an unchanged DB must not rewrite TASTE.md")
		}
		var after int
		_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type IN ('profile_add','profile_cut')`).Scan(&after)
		if after != before {
			t.Fatalf("an unchanged DB must write no new profile events: before=%d after=%d", before, after)
		}
	})
}

// TestSyncHeaderEventIDStable: two consecutive syncs with nothing new leave
// TASTE.md's mtime untouched — the header never churns on its own.
func TestSyncHeaderEventIDStable(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers stable headers", 0.6)
	if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	info1, err := os.Stat(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	info2, err := os.Stat(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("mtime moved on an unchanged DB: %v -> %v", info1.ModTime(), info2.ModTime())
	}
}

// TestTasteFileWithinBudget: the whole rendered file (its own header + body)
// stays within prewarm_session estimated tokens at 200 eligible rows, and
// the reserved budget is identical whether the store carries 99 or 100 live
// rows — the fixed worst-case header width (req 1, red-team L3) must never
// let a digit boundary shift the estimate.
func TestTasteFileWithinBudget(t *testing.T) {
	seed := func(t *testing.T, n int) (*sql.DB, config.Config) {
		db, cfg := newHomeCfg(t)
		for i := 0; i < n; i++ {
			seedStated(t, db, cfg, "General", fmt.Sprintf("Prefers row number %d with padding text to cost real tokens", i), 0.5)
		}
		return db, cfg
	}
	_, cfg99 := seed(t, 99)
	_, cfg100 := seed(t, 100)
	if ProfileBudget(cfg99) != ProfileBudget(cfg100) {
		t.Fatalf("the profile budget must not depend on N crossing a digit boundary: N=99 -> %d, N=100 -> %d", ProfileBudget(cfg99), ProfileBudget(cfg100))
	}

	db200, cfg200 := newHomeCfg(t)
	for i := 0; i < 200; i++ {
		seedStated(t, db200, cfg200, "General", fmt.Sprintf("Prefers row number %d with padding text to cost real tokens", i), 0.5)
	}
	full, err := Render(db200, cfg200, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := learn.EstimateTokens(full); got > cfg200.TokenBudgets.PrewarmSession {
		t.Fatalf("TASTE.md over budget at 200 eligible rows: %d > %d", got, cfg200.TokenBudgets.PrewarmSession)
	}
}
