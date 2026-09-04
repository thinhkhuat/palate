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
