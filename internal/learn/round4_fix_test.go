package learn

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/storage"
)

// A row another process closes between Restore's read and its write must not
// be reported as restored: Restore says the row is still closed and fails.
// The second connection holds the write lock while Restore reads, then commits
// the close while Restore's write is waiting for the lock.
func TestRestoreReportsRealOutcome(t *testing.T) {
	storage.Close()
	path := filepath.Join(t.TempDir(), "palate.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storage.Close)
	insertPrefFull(t, db, "D", "General", "Prefers tabs", "DISTILLED", 0.5, "[]")

	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	c, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(ctx, `UPDATE preferences SET valid_until=? WHERE id='D'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(400 * time.Millisecond)
		_, err := c.ExecContext(ctx, `COMMIT`)
		committed <- err
	}()

	_, rerr := Restore(db, "D")
	if cerr := <-committed; cerr != nil {
		t.Fatal(cerr)
	}
	if rerr == nil || !strings.Contains(rerr.Error(), "still closed") {
		t.Fatalf("a restore that left the row closed must say so, got %v", rerr)
	}
	var live int
	_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE id='D' AND valid_until IS NULL`).Scan(&live)
	if live != 0 {
		t.Fatal("setup: the other connection's close should stand")
	}
	if n := countEvents(t, db, "restore"); n != 0 {
		t.Fatalf("restore events = %d, want 0 for a restore that changed nothing", n)
	}
}

func countEvents(t *testing.T, db *sql.DB, typ string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE event_type=?`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A restore of a stale-retired row that loses the race to a close must leave
// the row exactly as it was: no tier change, no tag change, no restore event.
func TestLostRestoreLeavesRowUnchanged(t *testing.T) {
	storage.Close()
	path := filepath.Join(t.TempDir(), "palate.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storage.Close)
	insertPrefFull(t, db, "D", "General", "Prefers tabs", "DISTILLED", 0.5, `["project_specific"]`)
	closedAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE id='D'`, closedAt); err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEvent(db, "consolidate_retire", `{"id":"D","reason":"`+StaleCandidateReasonPrefix+` one session"}`); err != nil {
		t.Fatal(err)
	}

	// A close from another process lands right after Restore clears valid_until:
	// the trigger closes the row again as soon as it is reopened.
	if _, err := db.Exec(`CREATE TRIGGER close_again AFTER UPDATE OF valid_until ON preferences WHEN NEW.valid_until IS NULL BEGIN UPDATE preferences SET valid_until='` + closedAt + `' WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}

	_, rerr := Restore(db, "D")
	if rerr == nil || !strings.Contains(rerr.Error(), "still closed") {
		t.Fatalf("a lost restore must say the row is still closed, got %v", rerr)
	}
	var tier, prov string
	var live int
	_ = db.QueryRow(`SELECT trust_tier, provenance, valid_until IS NULL FROM preferences WHERE id='D'`).Scan(&tier, &prov, &live)
	if live != 0 || tier != "DISTILLED" || !strings.Contains(prov, "project_specific") {
		t.Fatalf("a lost restore changed the row: live=%d tier=%s provenance=%s", live, tier, prov)
	}
	if n := countEvents(t, db, "restore"); n != 0 {
		t.Fatalf("restore events = %d, want 0", n)
	}
}
