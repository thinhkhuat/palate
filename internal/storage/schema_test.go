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
