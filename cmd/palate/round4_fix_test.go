package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/render"
)

// runCLIStderr is runCLI that also returns what the command wrote to stderr.
func runCLIStderr(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	stdout, err = runCLI(t, args...)
	os.Stderr = orig
	_ = w.Close()
	return stdout, <-done, err
}

// heldHeaderRemovedEdit leaves a header-removed, hand-added hold pending.
func heldHeaderRemovedEdit(t *testing.T) {
	t.Helper()
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "Prefers a seed row to render the file"); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	cfg := loadConfig()
	b, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	noHeader := strings.SplitN(string(b), "\n", 2)[1]
	if err := os.WriteFile(cfg.TastePath(), []byte(noHeader+"\n## Testing\n\n- Prefers a hand-added row while held. Confidence: 0.60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"s2","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	if !render.HoldPending(cfg) {
		t.Fatal("setup: the header-removed edit must be held")
	}
}

func assertReadOnlyViewKeepsHold(t *testing.T, flag string) {
	t.Helper()
	heldHeaderRemovedEdit(t)
	cfg := loadConfig()
	_, stderr, err := runCLIStderr(t, "taste", flag)
	if err != nil {
		t.Fatal(err)
	}
	if !render.HoldPending(cfg) {
		t.Fatalf("`palate taste %s` discarded the pending hold", flag)
	}
	if got := strings.Count(strings.TrimSpace(stderr), "\n"); got != 0 || !strings.Contains(stderr, "held") || !strings.Contains(stderr, "--accept-edits") {
		t.Fatalf("want one warning line naming the hold and how to resolve it, got %q", stderr)
	}
}

func TestCutLeavesHeldEditAlone(t *testing.T)  { assertReadOnlyViewKeepsHold(t, "--cut") }
func TestPathLeavesHeldEditAlone(t *testing.T) { assertReadOnlyViewKeepsHold(t, "--path") }

// A hold that can only be discarded must not be advertised as acceptable by
// the read-only views' warning.
func TestCutWarningForDiscardOnlyHold(t *testing.T) {
	heldHeaderRemovedEdit(t)
	cfg := loadConfig()
	if err := os.WriteFile(cfg.StatePath("readback.hold.json"), []byte(`{"held_path": "oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !render.HoldPending(cfg) || render.HoldAcceptable(cfg) {
		t.Fatal("setup: want a pending hold that can only be discarded")
	}
	_, stderr, err := runCLIStderr(t, "taste", "--cut")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "--accept-edits") || !strings.Contains(stderr, "held") {
		t.Fatalf("warning for a discard-only hold = %q, want the hold named and no --accept-edits", stderr)
	}
}
