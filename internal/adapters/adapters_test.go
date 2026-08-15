package adapters

import "testing"

func TestRegistryHonest(t *testing.T) {
	for _, a := range Registry {
		if a.Name == "Pi" || a.Name == "OpenClaw" || a.Name == "OpenCode" {
			if a.Capture != "recall-only (hooks unverified)" {
				t.Fatalf("%s should be recall-only until TASK-900 passes: %q", a.Name, a.Capture)
			}
		}
	}
}
