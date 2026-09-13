package distill

import (
	"path/filepath"
	"testing"
)

func TestReaderCodex(t *testing.T) {
	r := codexReader{}
	if r.Harness() != "codex" {
		t.Fatalf("Harness() = %q, want codex", r.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "codex_session.jsonl", filepath.Join(dir, "rollout-fixture.jsonl"))

	transcripts, err := r.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(transcripts) != 1 {
		t.Fatalf("Discover found %d transcripts, want 1", len(transcripts))
	}
	if transcripts[0].Project != "/Users/testuser/project" {
		t.Fatalf("Project = %q, want the session_meta line's cwd", transcripts[0].Project)
	}

	msgs, err := r.Messages(transcripts[0])
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("Messages returned %d entries, want 2 (developer + reasoning items excluded, corrupt line skipped): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "prefers small, focused commits" {
		t.Fatalf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Understood — I'll keep commits small and focused." {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
	for _, m := range msgs {
		if m.Role == "developer" {
			t.Fatalf("a developer-role line leaked through: %+v", m)
		}
	}
}
