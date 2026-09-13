package distill

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// buildOpenCodeFixtureDB creates a throwaway sqlite db shaped like
// OpenCode's own schema (session/message/part), including a synthetic
// role:"user" row that must never surface as a learnable message.
func buildOpenCodeFixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer conn.Close()

	ddl := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
	}
	for _, stmt := range ddl {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("ddl %q: %v", stmt, err)
		}
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec(`INSERT INTO session(id, directory) VALUES (?, ?)`, "ses_fixture", "/Users/testuser/project")

	exec(`INSERT INTO message(id, session_id, time_created, data) VALUES (?,?,?,?)`, "msg1", "ses_fixture", 1000, `{"role":"user"}`)
	exec(`INSERT INTO part(id, message_id, time_created, data) VALUES (?,?,?,?)`, "part1", "msg1", 1000,
		`{"type":"text","text":"The following tool was executed by the user","synthetic":true}`)

	exec(`INSERT INTO message(id, session_id, time_created, data) VALUES (?,?,?,?)`, "msg2", "ses_fixture", 2000, `{"role":"user"}`)
	exec(`INSERT INTO part(id, message_id, time_created, data) VALUES (?,?,?,?)`, "part2", "msg2", 2000,
		`{"type":"text","text":"prefers dependency injection over global singletons"}`)

	exec(`INSERT INTO message(id, session_id, time_created, data) VALUES (?,?,?,?)`, "msg3", "ses_fixture", 3000, `{"role":"assistant"}`)
	exec(`INSERT INTO part(id, message_id, time_created, data) VALUES (?,?,?,?)`, "part3", "msg3", 3000,
		`{"type":"text","text":"Understood — I'll favor dependency injection."}`)
	exec(`INSERT INTO part(id, message_id, time_created, data) VALUES (?,?,?,?)`, "part3b", "msg3", 3001,
		`{"type":"tool","text":"ran a search"}`)

	return path
}

func TestReaderOpenCode(t *testing.T) {
	r := openCodeReader{}
	if r.Harness() != "opencode" {
		t.Fatalf("Harness() = %q, want opencode", r.Harness())
	}

	dbPath := buildOpenCodeFixtureDB(t)

	transcripts, err := r.Discover(dbPath)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(transcripts) != 1 {
		t.Fatalf("Discover found %d transcripts, want 1", len(transcripts))
	}
	if transcripts[0].Project != "/Users/testuser/project" {
		t.Fatalf("Project = %q, want session.directory", transcripts[0].Project)
	}
	if transcripts[0].Path != "opencode:"+dbPath+":ses_fixture" {
		t.Fatalf("Path = %q, want the opencode:<db>:<session> form", transcripts[0].Path)
	}

	msgs, err := r.Messages(transcripts[0])
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("Messages returned %d entries, want 2 (synthetic row and the tool-type part excluded): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "prefers dependency injection over global singletons" {
		t.Fatalf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Understood — I'll favor dependency injection." {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
	for _, m := range msgs {
		if m.Content == "The following tool was executed by the user" {
			t.Fatalf("the synthetic row leaked through as a learnable message: %+v", m)
		}
	}
}
