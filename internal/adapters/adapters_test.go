package adapters

import "testing"

func TestRegistryHonest(t *testing.T) {
	for _, a := range Registry {
		switch a.Name {
		case "Pi", "OpenClaw", "OpenCode", "OMP (oh-my-pi)", "Hermes":
			if a.Capture != "recall-only (hooks unverified)" {
				t.Fatalf("%s should be recall-only until TASK-900 passes: %q", a.Name, a.Capture)
			}
		case "Claude Code", "Command Code", "ZCode":
			if !a.Verified {
				t.Fatalf("%s should be Verified=true (hook-based learning)", a.Name)
			}
		}
	}
}

func TestRegistryUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Registry {
		if seen[a.Name] {
			t.Fatalf("duplicate adapter name: %q", a.Name)
		}
		seen[a.Name] = true
	}
}
