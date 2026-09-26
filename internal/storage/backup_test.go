package storage

import (
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
