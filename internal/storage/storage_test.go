package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	Close()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { Close() })
	return db
}

func TestRememberRecall(t *testing.T) {
	db := openMem(t)
	if _, err := Remember(db, "hello palate", "", 0.5, "working_memory"); err != nil {
		t.Fatal(err)
	}
	results, err := Recall(db, "palate", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatalf("expected recall results")
	}
}

func TestSleepAdditive(t *testing.T) {
	db := openMem(t)
	for i := 0; i < 3; i++ {
		if _, err := Remember(db, "memory for sleep", "s1", 0.5, "working_memory"); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	_ = db.QueryRow(`SELECT count(*) FROM working_memory`).Scan(&before)
	n, err := Sleep(db)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("sleep claimed 0")
	}
	var episodic int
	_ = db.QueryRow(`SELECT count(*) FROM episodic_memory`).Scan(&episodic)
	if episodic == 0 {
		t.Fatalf("episodic empty after sleep")
	}
	var wmAfter int
	_ = db.QueryRow(`SELECT count(*) FROM working_memory`).Scan(&wmAfter)
	if wmAfter != before {
		t.Fatalf("sleep must not delete working rows (additive): before %d after %d", before, wmAfter)
	}
}

func TestFTSTrigger(t *testing.T) {
	db := openMem(t)
	id, _ := Remember(db, "dedup trigger content", "", 0.5, "working_memory")
	_, _ = db.Exec(`UPDATE working_memory SET importance=0.9 WHERE id=?`, id)
	results, _ := Recall(db, "dedup", 10, false)
	if len(results) == 0 {
		t.Fatalf("fts after update failed")
	}
}

func TestCaptureCounters(t *testing.T) {
	db := openMem(t)
	BumpHit(db, "user_diff")
	BumpHit(db, "user_diff")
	BumpDrop(db, "user_diff")
	var hits, drops int
	_ = db.QueryRow(`SELECT hits, drops FROM capture_counters WHERE signal_type='user_diff'`).Scan(&hits, &drops)
	if hits != 2 || drops != 1 {
		t.Fatalf("counters: hits=%d drops=%d", hits, drops)
	}
}

// The DSN must use the driver's _pragma dialect: a mattn-style key would be
// ignored and leave the DB in rollback-journal mode with no busy timeout.
func TestOpenAppliesWALAndBusyTimeout(t *testing.T) {
	Close()
	db, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(Close)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err %v; want wal", mode, err)
	}
	var timeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout < 5000 {
		t.Fatalf("busy_timeout = %d, err %v; want >= 5000", timeout, err)
	}
}
