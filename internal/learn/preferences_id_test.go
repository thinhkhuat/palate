package learn

import "testing"

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("duplicate id at call %d: %s", i, id)
		}
		seen[id] = true
	}
}
