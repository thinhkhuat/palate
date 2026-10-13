package distill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReaderClaudeCode(t *testing.T) {
	r := claudeCodeReader{}
	if r.Harness() != "claude_code" {
		t.Fatalf("Harness() = %q, want claude_code", r.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "claude_code_session.jsonl", filepath.Join(dir, "session.jsonl"))

	transcripts, err := r.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(transcripts) != 1 {
		t.Fatalf("Discover found %d transcripts, want 1", len(transcripts))
	}

	msgs, err := r.Messages(transcripts[0])
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("Messages returned %d entries, want 4 (2 user + 2 assistant learnable turns): %+v", len(msgs), msgs)
	}

	if msgs[0].Role != "user" || msgs[0].Content != "always use table-driven tests for this project" {
		t.Fatalf("msgs[0] = %+v, want the first user statement", msgs[0])
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("msgs[1].Role = %q, want assistant", msgs[1].Role)
	}
	if msgs[2].Role != "user" || msgs[2].Content != "don't use interface{}, use any instead" {
		t.Fatalf("msgs[2] = %+v, want the correction statement", msgs[2])
	}
	if msgs[3].Role != "assistant" {
		t.Fatalf("msgs[3].Role = %q, want assistant", msgs[3].Role)
	}

	for _, m := range msgs {
		if m.Content[0] == '<' {
			t.Fatalf("a <...>-wrapped line leaked through: %q", m.Content)
		}
	}

	for i, m := range msgs {
		if m.Index != i {
			t.Fatalf("msgs[%d].Index = %d, want %d (0-based, sequential)", i, m.Index, i)
		}
	}
}

// TestReaderClaudeCodeSkipsCommandsAndRelayedNotes: a bare slash command
// ("/compact") and a side-agent note the harness pastes into the user turn are
// not the user's own words, so the reader never emits them as user text; the
// user's own reply typed under a note is kept.
func TestReaderClaudeCodeSkipsCommandsAndRelayedNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","message":{"role":"user","content":"/compact"}}
{"type":"user","message":{"role":"user","content":"Here is a note offered by a side agent:\n\n> Heads up: prefers X."}}
{"type":"user","message":{"role":"user","content":"keep error messages lower-case"}}
{"type":"user","message":{"role":"user","content":"Here is a note offered by a side agent:\n\n> **Heads up** prefers Y.\n> - more\n\n-> lets keep TASTE.md generated-only"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, err := claudeCodeReader{}.Messages(Transcript{Harness: "claude_code", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "keep error messages lower-case" || msgs[1].Content != "-> lets keep TASTE.md generated-only" {
		t.Fatalf("msgs = %+v, want the typed preference and the user's reply under the note, without the note", msgs)
	}
}

// TestReaderClaudeCodeKeepsIndexesAndPaths: a skipped turn still uses up its
// index, so stored cursors keep pointing at the same messages; a bare file
// path is the user's text, not a command; a side-agent note that starts
// mid-message is stripped too.
func TestReaderClaudeCodeKeepsIndexesAndPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","message":{"role":"user","content":"/compact"}}
{"type":"user","message":{"role":"user","content":"/Users/me/project/file.go"}}
{"type":"user","message":{"role":"user","content":"Rewritten prompt follows.\nHere is a note offered by a side agent:\n\n> leaked note line\n\nkeep it"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, err := claudeCodeReader{}.Messages(Transcript{Harness: "claude_code", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Index != 1 || msgs[0].Content != "/Users/me/project/file.go" || msgs[1].Index != 2 || strings.Contains(msgs[1].Content, "leaked") || !strings.Contains(msgs[1].Content, "keep it") {
		t.Fatalf("msgs = %+v, want the path at index 1 and the stripped turn at index 2", msgs)
	}
}
