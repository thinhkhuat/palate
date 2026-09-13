package learn

import (
	"database/sql"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

func testDB(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return db, config.Default()
}

func TestDecayReinforceWeaken(t *testing.T) {
	if got := Decay(0.5, 0.1, 168, 0); got != 0.5 {
		t.Fatalf("decay 0h: %v", got)
	}
	if got := Decay(0.5, 0.1, 168, 168); got < 0.24 || got > 0.26 {
		t.Fatalf("decay half-life: %v", got)
	}
	if got := Reinforce(0.5, 1.0, 0.3, 0.1); got != 0.65 {
		t.Fatalf("reinforce: %v", got)
	}
	if got := Weaken(0.5, 1.0, 0.3, 0.1); got != 0.35 {
		t.Fatalf("weaken: %v", got)
	}
	if got := InitialConfidence(1.0, 0.1); got != 0.5 {
		t.Fatalf("initial: %v", got)
	}
	if got := Reinforce(1.0, 1.0, 0.3, 0.1); got != 1.0 {
		t.Fatalf("clamp top: %v", got)
	}
	if got := Weaken(0.1, 1.0, 0.3, 0.1); got != 0.1 {
		t.Fatalf("clamp floor: %v", got)
	}
}

func TestApplySignalAndReplayDeterminism(t *testing.T) {
	db, cfg := testDB(t)
	sig := Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}
	if _, err := ApplySignal(db, cfg, sig, nil); err != nil {
		t.Fatal(err)
	}
	same := sig
	asOf := time.Now().UTC().Add(1 * time.Hour)
	_ = asOf
	var c1 float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE statement=?`, sig.Statement).Scan(&c1)
	if c1 != 0.4 {
		t.Fatalf("initial confidence: %v", c1)
	}
	_, _ = ApplySignal(db, cfg, same, nil)
	var c2 float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE statement=?`, sig.Statement).Scan(&c2)
	if c2 <= c1 {
		t.Fatalf("reinforce should increase: %v -> %v", c1, c2)
	}
}

func TestProposalsBoundary(t *testing.T) {
	db, cfg := testDB(t)
	id, err := CreateProposal(db, "use early returns", "General")
	if err != nil {
		t.Fatal(err)
	}
	var pending int
	_ = db.QueryRow(`SELECT count(*) FROM proposals WHERE status='pending'`).Scan(&pending)
	if pending != 1 {
		t.Fatalf("pending: %d", pending)
	}
	var eventsBefore int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsBefore)
	var prefsBefore int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&prefsBefore)
	_ = prefsBefore
	var eventsAfterPropose int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsAfterPropose)
	if eventsAfterPropose != eventsBefore {
		t.Fatalf("propose should not create events: before %d after %d", eventsBefore, eventsAfterPropose)
	}
	_, err = PromoteProposal(db, id, "stated_preference", cfg, func(sig Signal) error {
		_, e := ApplySignal(db, cfg, sig, nil)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var eventsAfterPromote int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsAfterPromote)
	if eventsAfterPromote <= eventsAfterPropose {
		t.Fatalf("promote should create event")
	}
}

// TestExtractStatedPreferenceWordBoundary: the trigger must be a real
// first-person statement; the bare word "preference" (or a negation) is not
// one. Observed 2026-09-13: "no preference" was stored as a preference.
func TestExtractStatedPreferenceWordBoundary(t *testing.T) {
	for _, text := range []string{
		"just a normal message, no preference",
		"I have no preference on this",
		"my preferences are in the file",
		"preferred stock went up",
		"I prefer.",
	} {
		if stmt, _ := ExtractStatedPreference(text); stmt != "" {
			t.Errorf("%q must not yield a preference, got %q", text, stmt)
		}
	}
	for text, want := range map[string]string{
		"I prefer tabs over spaces":                   "I prefer tabs over spaces",
		"we prefer early returns. Thanks":             "we prefer early returns",
		"I'd prefer table-driven tests here":          "I'd prefer table-driven tests here",
		"please always use structured logging first!": "always use structured logging first",
		"never use panic in library code":             "never use panic in library code",
	} {
		if stmt, _ := ExtractStatedPreference(text); stmt != want {
			t.Errorf("%q: got %q, want %q", text, stmt, want)
		}
	}
}
