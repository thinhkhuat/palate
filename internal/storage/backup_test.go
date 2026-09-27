package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupBeforeCopiesTheDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "palate.db")
	if err := os.WriteFile(dbPath, []byte("sqlite bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	got, err := BackupBefore(dbPath, backups, "forget")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(got), "palate.db-") || !strings.HasSuffix(got, "-forget") {
		t.Fatalf("backup name must be palate.db-<stamp>-<label>: %q", got)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != "sqlite bytes" {
		t.Fatalf("backup content mismatch: %q %v", b, err)
	}
	// Windows has no Unix permission bits (Go reports 0666); privacy there is
	// the user profile's ACL, so the mode is only checkable elsewhere.
	if st, _ := os.Stat(got); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("backup must be 0600, got %o", st.Mode().Perm())
	}
	if NewestBackup(backups) != got {
		t.Fatalf("NewestBackup = %q, want %q", NewestBackup(backups), got)
	}

	// no DB yet → nothing to protect, no error, no file
	missing, err := BackupBefore(filepath.Join(dir, "absent.db"), backups, "pull")
	if err != nil || missing != "" {
		t.Fatalf("missing DB must be a no-op: %q %v", missing, err)
	}
}

// TestBackupIncludesUncheckpointedWrite: a write an open connection has
// committed but not checkpointed still lands in the -wal file, not the main
// DB file. BackupBefore must see it anyway (red-team M1) — VACUUM INTO reads
// through the WAL the way any other connection to the same path would,
// unlike a raw byte copy of the main file alone.
func TestBackupIncludesUncheckpointedWrite(t *testing.T) {
	Close()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "palate.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(Close)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement) VALUES('p1','General','Prefers table-driven tests')`); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&before); err != nil || before != 1 {
		t.Fatalf("seed row missing before backup: n=%d err=%v", before, err)
	}

	backups := filepath.Join(dir, "backups")
	got, err := BackupBefore(dbPath, backups, "wal-check")
	if err != nil || got == "" {
		t.Fatalf("BackupBefore: got=%q err=%v", got, err)
	}

	backupConn, err := sql.Open("sqlite", got)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupConn.Close()
	var after int
	if err := backupConn.QueryRow(`SELECT count(*) FROM preferences`).Scan(&after); err != nil {
		t.Fatalf("query backup: %v", err)
	}
	if after != before {
		t.Fatalf("backup row count = %d, want %d (the open connection's write must not be lost)", after, before)
	}
}

// TestFTSStaysInSyncOnStatementUpdate: refine/rewrite/merge update
// preferences.statement; the external-content FTS index must follow instead
// of raising SQLITE_CORRUPT_VTAB, and recall must find the new wording.
func TestFTSStaysInSyncOnStatementUpdate(t *testing.T) {
	Close()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement) VALUES('p1','Testing','Prefers table-driven tests.')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE preferences SET statement='Prefers golden-file tests.' WHERE id='p1'`); err != nil {
		t.Fatalf("statement update must not corrupt the FTS index: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM fts_preferences WHERE fts_preferences MATCH 'golden'`).Scan(&n)
	if n != 1 {
		t.Fatalf("FTS must index the new wording, got %d", n)
	}
	_ = db.QueryRow(`SELECT count(*) FROM fts_preferences WHERE fts_preferences MATCH 'table'`).Scan(&n)
	if n != 0 {
		t.Fatalf("FTS must drop the old wording, got %d", n)
	}
	if _, err := db.Exec(`DELETE FROM preferences WHERE id='p1'`); err != nil {
		t.Fatalf("delete must not corrupt the FTS index: %v", err)
	}
	var check string
	_ = db.QueryRow(`INSERT INTO fts_preferences(fts_preferences) VALUES('integrity-check')`).Scan(&check)
}
