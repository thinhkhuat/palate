package distill

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSpawnLearnSessionStartsDetached proves Start() returns without
// waiting on the child: a fake "bin" that sleeps briefly before doing
// anything must not make SpawnLearnSession itself block.
func TestSpawnLearnSessionStartsDetached(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "slow-bin.sh")
	if err := os.WriteFile(script, []byte("#!"+sh+"\nsleep 2\n"), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}

	start := time.Now()
	if err := SpawnLearnSession(script, filepath.Join(dir, "transcript.jsonl")); err != nil {
		t.Fatalf("SpawnLearnSession: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("SpawnLearnSession blocked for %v against a 2s-sleeping child; want near-instant Start()", elapsed)
	}
}

// TestSpawnLearnSessionSetsExplicitCWD proves the child runs in the
// parent's working directory even though cmd.Dir is set explicitly rather
// than left to ambient inheritance.
func TestSpawnLearnSessionSetsExplicitCWD(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "pwd-bin.sh")
	out := filepath.Join(dir, "out.txt")
	// $3 is the transcriptPath arg SpawnLearnSession always passes third
	// (after "learn" "session"), reused here as the script's output file.
	if err := os.WriteFile(script, []byte("#!"+sh+"\npwd > \"$3\"\n"), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}

	work := t.TempDir()
	t.Chdir(work)

	if err := SpawnLearnSession(script, out); err != nil {
		t.Fatalf("SpawnLearnSession: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(out)
		if err == nil {
			got = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == "" {
		t.Fatal("child never wrote its cwd — did it start at all?")
	}
	wantResolved, err := filepath.EvalSymlinks(work)
	if err != nil {
		wantResolved = work
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotResolved = got
	}
	if gotResolved != wantResolved {
		t.Fatalf("child ran in %q, want %q", got, work)
	}
}

func TestSpawnLearnSessionReturnsErrorForMissingBinary(t *testing.T) {
	dir := t.TempDir()
	err := SpawnLearnSession(filepath.Join(dir, "no-such-binary"), filepath.Join(dir, "transcript.jsonl"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent binary")
	}
}
