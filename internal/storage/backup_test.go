package storage

import (
	"os"
	"path/filepath"
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
	if st, _ := os.Stat(got); st.Mode().Perm() != 0o600 {
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
