package distill

import (
	"strings"
	"testing"
)

// TestLearnSessionCmdInheritsHome: the detached child gets the same
// environment as the hook that spawned it, so PALATE_HOME (and HOME) resolve
// to the same store.
func TestLearnSessionCmdInheritsHome(t *testing.T) {
	t.Setenv("PALATE_HOME", "/tmp/palate-home-for-child")
	cmd := learnSessionCmd("/bin/echo", "/tmp/t.jsonl")
	if cmd.Env == nil {
		t.Fatal("cmd.Env must be set explicitly")
	}
	found := false
	for _, kv := range cmd.Env {
		if kv == "PALATE_HOME=/tmp/palate-home-for-child" {
			found = true
		}
	}
	if !found {
		t.Fatalf("PALATE_HOME not handed to the child: %v", cmd.Env)
	}
	if cmd.Dir == "" {
		t.Fatal("cmd.Dir must be the spawning cwd")
	}
	if !strings.HasSuffix(cmd.Args[0], "echo") || cmd.Args[1] != "learn" || cmd.Args[2] != "session" {
		t.Fatalf("unexpected argv: %v", cmd.Args)
	}
}
