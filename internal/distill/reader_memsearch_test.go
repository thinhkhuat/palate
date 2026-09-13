package distill

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReaderMemsearch(t *testing.T) {
	r := memsearchReader{}
	if r.Harness() != "memsearch" {
		t.Fatalf("Harness() = %q, want memsearch", r.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "memsearch_journal.md", filepath.Join(dir, "2026-09-01.md"))

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
		t.Fatalf("Messages returned %d entries, want 3 (one per ### HH:MM block): %+v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.Role != "user" {
			t.Fatalf("memsearch Message.Role = %q, want user (binary Role; [session summary] labeling happens at format time)", m.Role)
		}
	}
	for i, m := range msgs {
		if m.Index != i {
			t.Fatalf("msgs[%d].Index = %d, want %d (identical HH:MM labels must not collide)", i, m.Index, i)
		}
	}
	if msgs[0].Content == "" || msgs[1].Content == "" || msgs[2].Content == "" {
		t.Fatalf("a block produced empty content: %+v", msgs)
	}

	rendered := FormatEvidence("memsearch", msgs)
	if !strings.Contains(rendered, "[session summary]") {
		t.Fatalf("FormatEvidence output missing the [session summary] label: %s", rendered)
	}
}
