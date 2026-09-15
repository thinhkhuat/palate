package consolidate

import (
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
)

// TestRetireVerbatimRows: gate G6 — a store poisoned with the exact garbage
// shapes found live (2026-09-15 audit: "prefers: <code>", regex literals,
// vendored bodies, JSON blobs) retires them in bulk with no 40%-guard and no
// gateway, while real prose preferences survive untouched.
func TestRetireVerbatimRows(t *testing.T) {
	db, cfg := testStore(t)
	verbatim := []string{
		"prefers: TABLE_SEP_RE = re.compile(r\"^\\s*\\|\") / NUMBERED_RE = re.compile(r\"x\")",
		"prefers: # VENDORED FROM _vn-family/scripts/check-scope-schema.py - family body; do not edit here",
		"prefers: \"version\": \"1.3.2\", / \"tarball\": \"1.3.2/vn-canu-bundle-1.3.2.tar.gz\"",
		"prefers: *Tiến độ triển khai Công điện số 64/CĐ-TTg tại các địa phương, test content here*",
		"def extract_dob(s: str) -> str | None: / m = re.search(r\"x\")",
	}
	var verbatimIDs []string
	for _, s := range verbatim {
		p, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: s, Category: "General"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		verbatimIDs = append(verbatimIDs, p.ID)
	}
	// Bulk: 5 of 7 rows (71%) — past the 40% Run guard on purpose.
	real, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers table-driven tests.", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	real2, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalLLMDistilled, Statement: "Wants commits pushed autonomously during a work arc.", Category: "Workflow", Weight: 0.8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if liveCount(db) != 7 {
		t.Fatalf("fixture: live=%d, want 7", liveCount(db))
	}
	ids, err := RetireVerbatimRows(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 5 {
		t.Fatalf("dry-run must find exactly the 5 verbatim rows, got %d: %v", len(ids), ids)
	}
	retired, err := RetireVerbatimRows(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 5 {
		t.Fatalf("must retire 5 verbatim rows, retired %d", len(retired))
	}
	for _, id := range verbatimIDs {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&n)
		if n != 0 {
			t.Fatalf("verbatim row %s still live", id)
		}
	}
	for _, id := range []string{real.ID, real2.ID} {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&n)
		if n != 1 {
			t.Fatalf("real preference %s (%q) must survive, live=%d", id, id, n)
		}
	}
	var events int
	_ = db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='consolidate_retire'`).Scan(&events)
	if events != 5 {
		t.Fatalf("each retire must pass the events funnel, consolidate_retire=%d", events)
	}
	if liveCount(db) != 2 {
		t.Fatalf("live=%d, want 2 survivors", liveCount(db))
	}
}
