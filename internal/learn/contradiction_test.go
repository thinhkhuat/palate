package learn

import (
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

func TestContradictionWeakensAndConflicts(t *testing.T) {
	cfg := config.Default()
	db, err := storage.Open(t.TempDir() + "/palate.db")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	first, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	second, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "never use table-driven tests", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var conflicts int
	if err := db.QueryRow(`SELECT count(*) FROM conflicts WHERE resolved_at IS NULL`).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 {
		t.Fatalf("expected 1 open conflict, got %d", conflicts)
	}

	var weakened float64
	if err := db.QueryRow(`SELECT confidence FROM preferences WHERE id=?`, first.ID).Scan(&weakened); err != nil {
		t.Fatal(err)
	}
	if weakened >= first.Confidence {
		t.Fatalf("existing preference was not weakened: before=%f after=%f", first.Confidence, weakened)
	}

	visible, err := ListPreferences(db, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range visible {
		if p.ID == first.ID || p.ID == second.ID {
			t.Fatalf("conflicted preference %q should be hidden when includeConflicted=false", p.ID)
		}
	}

	all, err := ListPreferences(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Fatalf("includeConflicted=true should show both rows, got %d", len(all))
	}
}

func TestSameStatementReinforces(t *testing.T) {
	cfg := config.Default()
	db, err := storage.Open(t.TempDir() + "/palate.db")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	first, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Confidence < first.Confidence {
		t.Fatalf("same statement should reinforce, not weaken: %f -> %f", first.Confidence, again.Confidence)
	}
	var conflicts int
	_ = db.QueryRow(`SELECT count(*) FROM conflicts WHERE resolved_at IS NULL`).Scan(&conflicts)
	if conflicts != 0 {
		t.Fatalf("same statement must not create a conflict, got %d", conflicts)
	}
}

// TestInsertConflictTier proves phase-01 req 3: a distilled contradiction's
// new side inserts at the DISTILLED tier (not STATED), and insertConflict
// appends its own "create" event (red-team L1).
func TestInsertConflictTier(t *testing.T) {
	cfg := config.Default()
	db, err := storage.Open(t.TempDir() + "/palate.db")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	first, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var createsBefore int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE event_type='create'`).Scan(&createsBefore); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplySignal(db, cfg, Signal{Type: SignalLLMDistilled, Statement: "never use table-driven tests", Category: "Testing", Weight: 0.5}, nil); err != nil {
		t.Fatal(err)
	}

	var newID, tier string
	if err := db.QueryRow(`SELECT id, trust_tier FROM preferences WHERE id != ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, first.ID).Scan(&newID, &tier); err != nil {
		t.Fatal(err)
	}
	if tier != "DISTILLED" {
		t.Fatalf("a distilled contradiction must insert DISTILLED, got %s", tier)
	}

	var createsAfter int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE event_type='create'`).Scan(&createsAfter); err != nil {
		t.Fatal(err)
	}
	if createsAfter != createsBefore+1 {
		t.Fatalf("insertConflict must append exactly one create event: before=%d after=%d", createsBefore, createsAfter)
	}

	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE event_type='create' AND json_extract(payload,'$.id')=?`, newID).Scan(&payload); err != nil {
		t.Fatalf("create event for the new conflicting row not found: %v", err)
	}
}

func TestIsContradictionTable(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"use table-driven tests", "never use table-driven tests", true},
		{"use table-driven tests", "use table-driven tests", false},
		{"always use named imports", "use default imports instead", true},
		{"prefer tabs", "prefer spaces", true},
		{"write tests first", "write tests first!", false},
		{"asof probe prefers tabs oldstatement", "conflict probe uses spaces", false},
	}
	for _, c := range cases {
		if got := isContradiction(c.a, c.b); got != c.want {
			t.Errorf("isContradiction(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
