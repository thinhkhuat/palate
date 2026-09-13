package storage

import (
	"database/sql"
	"testing"
)

// TestDirtyFlushOnClose: mutations mark the store dirty once; Close flushes
// exactly once; a Close with no writes flushes nothing.
func TestDirtyFlushOnClose(t *testing.T) {
	Close()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	flushes := 0
	SetOnDirtyFlush(func(*sql.DB) { flushes++ })
	t.Cleanup(func() { SetOnDirtyFlush(nil) })

	for i := 0; i < 3; i++ {
		if err := AppendEvent(db, "create", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	Close()
	if flushes != 1 {
		t.Fatalf("3 events + Close must flush once, got %d", flushes)
	}

	if _, err := Open(":memory:"); err != nil {
		t.Fatal(err)
	}
	Close()
	if flushes != 1 {
		t.Fatalf("Close with no writes must not flush, got %d", flushes)
	}
}

// TestFlushIfDirtyClearsTheFlag: a long-lived caller flushes per write and
// does not re-flush without a new mutation.
func TestFlushIfDirtyClearsTheFlag(t *testing.T) {
	Close()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	flushes := 0
	SetOnDirtyFlush(func(*sql.DB) { flushes++ })
	t.Cleanup(func() { SetOnDirtyFlush(nil) })

	FlushIfDirty()
	if flushes != 0 {
		t.Fatal("nothing dirty yet")
	}
	_ = AppendEvent(db, "create", `{}`)
	FlushIfDirty()
	FlushIfDirty()
	if flushes != 1 {
		t.Fatalf("one mutation must flush exactly once, got %d", flushes)
	}
}
