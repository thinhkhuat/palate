package distill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeClineSessionFixture writes a Cline-format session (metadata record +
// messages.json) into dir and returns the transcript path.
func writeClineSessionFixture(t *testing.T, dir, sessionID, cwd, messagesJSON string) string {
	t.Helper()
	sessDir := filepath.Join(dir, sessionID)
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{"session_id": sessionID, "cwd": cwd}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(sessDir, sessionID+".json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sessDir, sessionID+".messages.json")
	if err := os.WriteFile(p, []byte(messagesJSON), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClineReaderDiscoverAndMessages(t *testing.T) {
	dir := t.TempDir()
	msgs := `{"version":1,"sessionId":"s1","messages":[` +
		`{"id":"1","role":"user","ts":1,"content":[{"type":"text","text":"prefers early returns over nested if statements"}]},` +
		`{"id":"2","role":"assistant","ts":2,"content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":"Noted — I'll favor early returns."},{"type":"tool_use","name":"write_to_file","input":{"path":"/tmp/x"}}]},` +
		`{"id":"3","role":"user","ts":3,"content":[{"type":"tool_result","content":"wrote file"}]},` +
		`{"id":"4","role":"assistant","ts":4,"content":[{"type":"text","text":"Ready to continue."}]}` +
		`]}`
	writeClineSessionFixture(t, dir, "s1", "/Users/testuser/project", msgs)
	// A directory without a messages.json must not match the glob.
	if err := os.MkdirAll(filepath.Join(dir, "s2"), 0755); err != nil {
		t.Fatal(err)
	}

	r := clineReader{}
	if r.Harness() != "cline" {
		t.Fatalf("Harness() = %q, want cline", r.Harness())
	}
	transcripts, err := r.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(transcripts) != 1 {
		t.Fatalf("Discover found %d transcripts, want 1", len(transcripts))
	}
	if transcripts[0].Project != "/Users/testuser/project" {
		t.Fatalf("Project = %q, want the session record's cwd", transcripts[0].Project)
	}
	if transcripts[0].Path == "" || transcripts[0].ModTime.IsZero() {
		t.Fatalf("transcript path/modtime unset: %+v", transcripts[0])
	}

	got, err := r.Messages(transcripts[0])
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Messages returned %d entries, want 3 (tool-only turns excluded, thinking stripped): %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "prefers early returns over nested if statements" {
		t.Fatalf("msgs[0] = %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "Noted — I'll favor early returns." {
		t.Fatalf("msgs[1] = %+v (thinking and tool_use blocks must not leak)", got[1])
	}
	if got[2].Content != "Ready to continue." {
		t.Fatalf("msgs[2] = %+v", got[2])
	}
}

func TestClineReaderRealCorpus(t *testing.T) {
	// Against the live ~/.cline/data/sessions corpus: every discovered
	// transcript must parse with roles strictly user/assistant and Index
	// monotonically increasing.
	dir := filepath.Join(homeDir(), ".cline", "data", "sessions")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Skip("no live Cline corpus on this machine")
	}
	r := clineReader{}
	transcripts, err := r.Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(transcripts) == 0 {
		t.Fatal("live corpus exists but Discover found nothing")
	}
	if len(transcripts) > 200 {
		transcripts = transcripts[:200]
	}
	for _, tr := range transcripts {
		msgs, err := r.Messages(tr)
		if err != nil {
			t.Fatalf("Messages(%s): %v", tr.Path, err)
		}
		prev := -1
		for _, m := range msgs {
			if m.Role != "user" && m.Role != "assistant" {
				t.Fatalf("%s: role %q leaked into Messages", tr.Path, m.Role)
			}
			if m.Index <= prev {
				t.Fatalf("%s: Index not strictly increasing (%d after %d)", tr.Path, m.Index, prev)
			}
			prev = m.Index
		}
	}
}

// TestClineReaderInAllReaders guards the registration — a reader left out of
// AllReaders is silently dead for `palate learn sessions`.
func TestClineReaderInAllReaders(t *testing.T) {
	for _, r := range AllReaders() {
		if r.Harness() == "cline" {
			return
		}
	}
	t.Fatal("clineReader missing from AllReaders()")
}
