package verify

import (
	"testing"

	"github.com/thinhkhuat/palate/internal/storage"
)

func TestScenariosHashStable(t *testing.T) {
	a := ScenariosHash()
	b := ScenariosHash()
	if a != b || a == "" {
		t.Fatalf("hash unstable: %q %q", a, b)
	}
}

func TestPureFlag(t *testing.T) {
	storage.Close()
	db, _ := storage.Open(":memory:")
	t.Cleanup(func() { storage.Close() })
	rep, err := RunCapture(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pure {
		t.Fatalf("pure flag not propagated")
	}
	if rep.ScenariosHash == "" {
		t.Fatalf("hash empty")
	}
}
