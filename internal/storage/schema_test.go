package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// Two backups in the same minute keep the first snapshot: it is the state a
// restore wants, and a loop of destructive verbs must not erase it.
func TestBackupBeforeKeepsFirstSnapshotInMinute(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "palate.db")
	if err := os.WriteFile(db, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	b1, err := BackupBefore(db, filepath.Join(dir, "backups"), "t")
	if err != nil || b1 == "" {
		t.Fatalf("first backup: %v %q", err, b1)
	}
	_ = os.WriteFile(db, []byte("second"), 0o600)
	b2, err := BackupBefore(db, filepath.Join(dir, "backups"), "t")
	if err != nil || b2 != b1 {
		t.Fatalf("second backup in the same minute: %v %q (want %q)", err, b2, b1)
	}
		got, _ := os.ReadFile(b1)
	if string(got) != "first" {
		t.Fatalf("first snapshot overwritten: %q", got)
	}
}

// TestLegacyScaffoldTablesDropped: facts/triples/graph_edges are dead
// scaffold — no code path ever read or wrote them (2026-09-16 verdict).
// Opening a store that still carries them (with rows!) drops them
// idempotently; every load-bearing table survives.
func TestLegacyScaffoldTablesDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS triples (id TEXT PRIMARY KEY, subject TEXT, predicate TEXT, object TEXT)`,
		`CREATE TABLE IF NOT EXISTS facts (id TEXT PRIMARY KEY, content TEXT)`,
		`CREATE TABLE IF NOT EXISTS graph_edges (id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO triples(id, subject, predicate, object) VALUES('t1','s','p','o')`); err != nil {
		t.Fatal(err)
	}
	Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	for _, table := range []string{"triples", "facts", "graph_edges"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("legacy scaffold table %q must be dropped on open", table)
		}
	}
	for _, table := range []string{"working_memory", "episodic_memory", "scratchpad", "preferences", "events", "memory_embeddings", "proposals"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("load-bearing table %q must survive the hygiene pass", table)
		}
	}
}
