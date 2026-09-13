package distill

import (
	"path/filepath"
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
