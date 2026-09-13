package distill

import (
	"database/sql"
	"testing"

	"github.com/thinhkhuat/palate/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return db
}

func TestCursorRoundTrip(t *testing.T) {
	db := testDB(t)

	if _, _, ok := GetCursor(db, "git", "repo-a"); ok {
		t.Fatalf("GetCursor on an unseen (source, key): ok = true, want false")
	}

	if err := SetCursor(db, "git", "repo-a", "abc123"); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	pos, attempts, ok := GetCursor(db, "git", "repo-a")
	if !ok {
		t.Fatalf("GetCursor after SetCursor: ok = false")
	}
	if pos != "abc123" {
		t.Fatalf("position = %q, want %q", pos, "abc123")
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0 after SetCursor", attempts)
	}
}

func TestCursorSetResetsAttempts(t *testing.T) {
	db := testDB(t)

	if _, err := RecordFailedAttempt(db, "claude_code", "transcript-1"); err != nil {
		t.Fatalf("RecordFailedAttempt: %v", err)
	}
	if _, err := RecordFailedAttempt(db, "claude_code", "transcript-1"); err != nil {
		t.Fatalf("RecordFailedAttempt: %v", err)
	}
	_, attempts, ok := GetCursor(db, "claude_code", "transcript-1")
	if !ok || attempts != 2 {
		t.Fatalf("attempts after 2 failures = %d (ok=%v), want 2", attempts, ok)
	}

	if err := SetCursor(db, "claude_code", "transcript-1", "line-42"); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	pos, attempts, ok := GetCursor(db, "claude_code", "transcript-1")
	if !ok || pos != "line-42" || attempts != 0 {
		t.Fatalf("after SetCursor: pos=%q attempts=%d ok=%v, want pos=line-42 attempts=0 ok=true", pos, attempts, ok)
	}
}

func TestCursorRecordFailedAttemptIncrements(t *testing.T) {
	db := testDB(t)

	for i := 1; i <= 3; i++ {
		attempts, err := RecordFailedAttempt(db, "memsearch", "journal-1")
		if err != nil {
			t.Fatalf("RecordFailedAttempt #%d: %v", i, err)
		}
		if attempts != i {
			t.Fatalf("RecordFailedAttempt #%d returned %d, want %d", i, attempts, i)
		}
	}
}

func TestCursorSourceKeyIndependent(t *testing.T) {
	db := testDB(t)

	if err := SetCursor(db, "git", "repo-a", "sha-1"); err != nil {
		t.Fatal(err)
	}
	if err := SetCursor(db, "git", "repo-b", "sha-2"); err != nil {
		t.Fatal(err)
	}
	if err := SetCursor(db, "claude_code", "repo-a", "line-9"); err != nil {
		t.Fatal(err)
	}

	posA, _, _ := GetCursor(db, "git", "repo-a")
	posB, _, _ := GetCursor(db, "git", "repo-b")
	posC, _, _ := GetCursor(db, "claude_code", "repo-a")
	if posA != "sha-1" || posB != "sha-2" || posC != "line-9" {
		t.Fatalf("cursors collided across (source,key): a=%q b=%q c=%q", posA, posB, posC)
	}
}
