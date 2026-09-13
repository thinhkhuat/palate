package learn

import "testing"

// TestNearDuplicateReinforces checks the plan's own canonical pair: a
// llm_distilled restatement of an existing preference, tokenized and
// compared at nearDuplicateThreshold, reinforces the existing row instead
// of inserting a near-twin.
func TestNearDuplicateReinforces(t *testing.T) {
	db, cfg := testDB(t)

	first, err := ApplySignal(db, cfg, Signal{
		Type:      SignalLLMDistilled,
		Statement: "Prefers table-driven tests.",
		Category:  "Testing",
		Weight:    0.9,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	second, err := ApplySignal(db, cfg, Signal{
		Type:      SignalLLMDistilled,
		Statement: "Prefer table driven tests in Go",
		Category:  "Testing",
		Weight:    0.9,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != first.ID {
		t.Fatalf("near-duplicate restatement created a new row: first=%s second=%s", first.ID, second.ID)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("preferences count = %d, want 1 (reinforced, not duplicated)", count)
	}

	var reinforcementCount int
	if err := db.QueryRow(`SELECT reinforcement_count FROM preferences WHERE id=?`, first.ID).Scan(&reinforcementCount); err != nil {
		t.Fatal(err)
	}
	if reinforcementCount != 1 {
		t.Fatalf("reinforcement_count = %d, want 1", reinforcementCount)
	}
}

func TestFindNearDuplicateJaccardExactly(t *testing.T) {
	a := nearDupWords("Prefers table-driven tests.")
	b := nearDupWords("Prefer table driven tests in Go")
	got := jaccard(a, b)
	if got < nearDuplicateThreshold {
		t.Fatalf("canonical pair jaccard = %f, want >= %f (threshold)", got, nearDuplicateThreshold)
	}
	if got != 0.75 {
		t.Fatalf("canonical pair jaccard = %f, want 0.75 exactly (3 shared / 4 union tokens)", got)
	}
}

// A distilled statement seeds at the model's confidence, clamped to
// [c_floor, 0.9], instead of the halved seed other signals get.
func TestDistilledSeedUsesModelConfidence(t *testing.T) {
	for _, tc := range []struct{ w, want float64 }{{0.85, 0.85}, {0.99, 0.9}, {0.01, 0.1}} {
		if got := distilledSeed(tc.w, 0.1); got != tc.want {
			t.Fatalf("distilledSeed(%v) = %v, want %v", tc.w, got, tc.want)
		}
	}
}
