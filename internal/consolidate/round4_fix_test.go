package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// A promote committed by a second connection between the rewrite's read and
// its write must stop the rewrite as a refusal: the row is STATED now and its text
// is never rewritten, so there is nothing for a proposal to apply. The second connection takes the write lock before the rewrite
// reads, commits the promote while the rewrite's UPDATE is waiting for the
// lock, so the rewrite has read DISTILLED and writes after STATED committed.
func TestRewriteOfNewlyStatedIsRefused(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	insertRow(t, db, rowSpec{id: "K", stmt: "Writes tests before the code always", conf: 0.8, sessions: []string{"sess-a"}})

	other, err := sql.Open("sqlite", "file:"+cfg.DBPath+"?_pragma=busy_timeout(5000)")
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
	if _, err := c.ExecContext(ctx, `UPDATE preferences SET trust_tier='STATED' WHERE id='K'`); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(400 * time.Millisecond)
		_, err := c.ExecContext(ctx, `COMMIT`)
		committed <- err
	}()

	err = applyAction(db, cfg, Action{Type: "rewrite", ID: "K", Statement: "Writes tests first.", Reason: "tighter"}, true)
	if cerr := <-committed; cerr != nil {
		t.Fatal(cerr)
	}
	if !errors.Is(err, errActionRefused) || errors.Is(err, errActionClosed) {
		t.Fatalf("a rewrite of a row promoted mid-write must be refused (STATED text is never rewritten, so no proposal), got %v", err)
	}
	var stmt, tier string
	_ = db.QueryRow(`SELECT statement, trust_tier FROM preferences WHERE id='K'`).Scan(&stmt, &tier)
	if tier != "STATED" || stmt != "Writes tests before the code always" {
		t.Fatalf("STATED text was rewritten: tier=%s statement=%q", tier, stmt)
	}
	if n := rowCount(db, "events WHERE event_type='consolidate_rewrite'"); n != 0 {
		t.Fatalf("consolidate_rewrite events = %d, want 0", n)
	}
}
