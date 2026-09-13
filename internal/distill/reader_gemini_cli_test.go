package distill

import (
	"path/filepath"
	"testing"
)

func TestReaderGeminiCLI(t *testing.T) {
	r := geminiCLIReader{}
	if r.Harness() != "gemini_cli" {
		t.Fatalf("Harness() = %q, want gemini_cli", r.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "gemini_cli_session.jsonl", filepath.Join(dir, "session.jsonl"))

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
	if len(msgs) != 1 {
		t.Fatalf("Messages returned %d entries, want 1 (last-line-wins, model-role entries excluded): %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "prefers concise commit messages" {
		t.Fatalf("msgs[0].Content = %q, want the last snapshot's message, not the superseded one", msgs[0].Content)
	}
}
