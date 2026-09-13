package distill

import (
	"path/filepath"
	"testing"
)

func TestReaderEnvelopeCommandCodeAndPi(t *testing.T) {
	commandCode := newCommandCodeReader()
	pi := newPiReader()
	if commandCode.Harness() != "command_code" {
		t.Fatalf("command code Harness() = %q, want command_code", commandCode.Harness())
	}
	if pi.Harness() != "pi" {
		t.Fatalf("pi Harness() = %q, want pi", pi.Harness())
	}

	dir := t.TempDir()
	copyFixture(t, "envelope_session.jsonl", filepath.Join(dir, "session.jsonl"))
	copyFixture(t, "envelope_session.jsonl", filepath.Join(dir, "session.checkpoints.jsonl"))

	for _, r := range []Reader{commandCode, pi} {
		transcripts, err := r.Discover(dir)
		if err != nil {
			t.Fatalf("%s Discover: %v", r.Harness(), err)
		}
		if len(transcripts) != 1 {
			t.Fatalf("%s Discover found %d transcripts, want 1 (*.checkpoints.jsonl excluded)", r.Harness(), len(transcripts))
		}
		if transcripts[0].Project != "/Users/testuser/project" {
			t.Fatalf("%s Project = %q, want the session line's cwd", r.Harness(), transcripts[0].Project)
		}

		msgs, err := r.Messages(transcripts[0])
		if err != nil {
			t.Fatalf("%s Messages: %v", r.Harness(), err)
		}
		if len(msgs) != 3 {
			t.Fatalf("%s Messages returned %d entries, want 3 (tool_use/tool_result-only turns excluded, corrupt line skipped): %+v", r.Harness(), len(msgs), msgs)
		}
		if msgs[0].Role != "user" || msgs[0].Content != "prefers early returns over nested if statements" {
			t.Fatalf("%s msgs[0] = %+v", r.Harness(), msgs[0])
		}
		if msgs[1].Role != "assistant" || msgs[1].Content != "Noted — I'll favor early returns." {
			t.Fatalf("%s msgs[1] = %+v (tool_use block must not leak into Content)", r.Harness(), msgs[1])
		}
		if msgs[2].Content != "Ready to continue." {
			t.Fatalf("%s msgs[2] = %+v", r.Harness(), msgs[2])
		}
	}
}
