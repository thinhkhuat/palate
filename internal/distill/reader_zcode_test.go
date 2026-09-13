package distill

import (
	"path/filepath"
	"testing"
)

func TestReaderZCode(t *testing.T) {
	r := zcodeReader{}
	if r.Harness() != "zcode" {
		t.Fatalf("Harness() = %q, want zcode", r.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "zcode_session.jsonl", filepath.Join(dir, "model-io-fixture.jsonl"))

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
	if len(msgs) != 3 {
		t.Fatalf("Messages returned %d entries, want 3 (lite line excluded, cumulative dupes unioned, corrupt line skipped): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "prefers explicit error handling over silently swallowing errors" {
		t.Fatalf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Got it — I'll handle errors explicitly." {
		t.Fatalf("msgs[1] = %+v (reasoning block must not leak into Content)", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Content != "also avoid global mutable state" {
		t.Fatalf("msgs[2] = %+v", msgs[2])
	}
	for _, m := range msgs {
		if m.Content == "ran a lint check" {
			t.Fatalf("a tool-role message leaked through: %+v", m)
		}
	}
}
