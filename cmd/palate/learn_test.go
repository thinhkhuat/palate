package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/distill"
)

// captureStdout runs fn with os.Stdout swapped for a pipe and returns what
// it printed — discoverForReader prints the "including global memsearch
// journals" notice directly, outside the cobra command tree.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func writeJournal(t *testing.T, dir, name, block string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "## Session 09:00\n\n### 09:00\n<!-- session:s turn:t transcript:/tmp/fixture.jsonl -->\n" + block + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestMemsearchGlobalOptIn(t *testing.T) {
	projectDir := t.TempDir()
	homeDir := t.TempDir()
	t.Chdir(projectDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir) // os.UserHomeDir reads this on Windows

	writeJournal(t, filepath.Join(projectDir, ".memsearch", "memory"), "2026-09-01.md", "- a project-local note")
	writeJournal(t, filepath.Join(homeDir, ".memsearch", "memory"), "2026-09-01.md", "- a global note from another project")

	reader, err := readerByHarness("memsearch")
	if err != nil {
		t.Fatalf("readerByHarness(memsearch): %v", err)
	}

	local, err := discoverForReader(reader, "", false)
	if err != nil {
		t.Fatalf("discoverForReader (no --include-global): %v", err)
	}
	if len(local) != 1 {
		t.Fatalf("discoverForReader without --include-global found %d transcripts, want 1 (~/.memsearch/memory/ must never be touched)", len(local))
	}

	var out string
	var both []distill.Transcript
	out = captureStdout(t, func() {
		both, err = discoverForReader(reader, "", true)
	})
	if err != nil {
		t.Fatalf("discoverForReader (--include-global): %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("discoverForReader with --include-global found %d transcripts, want 2 (project-local + global)", len(both))
	}
	if !strings.Contains(out, "including global memsearch journals:") {
		t.Fatalf("--include-global must print a notice before touching ~/.memsearch/memory/, got: %q", out)
	}
}

func TestZCodeExperimentalOptIn(t *testing.T) {
	for _, r := range distill.AllReaders() {
		if r.Harness() == "zcode" {
			t.Fatalf("AllReaders() includes zcode — it must only appear via ExperimentalReaders()/--include-experimental")
		}
	}
	found := false
	for _, r := range distill.ExperimentalReaders() {
		if r.Harness() == "zcode" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ExperimentalReaders() does not include zcode")
	}

	reader, err := readerByHarness("zcode")
	if err != nil || reader.Harness() != "zcode" {
		t.Fatalf("readerByHarness(zcode) = %v, %v, want the zcode reader (still resolvable via --harness zcode on `learn session`)", reader, err)
	}
}

func TestSessionsCap(t *testing.T) {
	// wantOrder[i] is the Path expected at position i after capping — built
	// most-recent-first, then shuffled before capping so the test actually
	// exercises the sort, not just the truncation.
	base := time.Now()
	wantOrder := make([]distill.Transcript, 25)
	for i := range wantOrder {
		wantOrder[i] = distill.Transcript{
			Harness: "claude_code",
			Path:    filepath.Join("fixture", "session", string(rune('a'+i))+".jsonl"),
			ModTime: base.Add(-time.Duration(i) * time.Hour), // i=0 is most recent
		}
	}
	shuffled := make([]distill.Transcript, len(wantOrder))
	for i, tr := range wantOrder {
		shuffled[len(wantOrder)-1-i] = tr // reverse: oldest first going in
	}

	capped := capTranscripts(append([]distill.Transcript(nil), shuffled...), false)
	if len(capped) != maxTranscriptsPerHarness {
		t.Fatalf("capTranscripts(all=false) returned %d transcripts, want %d even though 25 exist", len(capped), maxTranscriptsPerHarness)
	}
	for i, tr := range capped {
		if tr.Path != wantOrder[i].Path {
			t.Fatalf("capTranscripts[%d] = %q, want %q (most-recently-modified first)", i, tr.Path, wantOrder[i].Path)
		}
	}

	all := capTranscripts(append([]distill.Transcript(nil), shuffled...), true)
	if len(all) != 25 {
		t.Fatalf("capTranscripts(all=true) returned %d transcripts, want all 25", len(all))
	}
}
