package render

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A hold filed by the 10-line cap lists its lines before anything is applied
// and needs --yes, the same as every other accept that brings lines in.
func TestLineCapHoldListsAndNeedsYes(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
	mustSync(t, db, cfg)
	var add strings.Builder
	var lines []string
	add.WriteString("\n## Testing\n\n")
	for i := 0; i < 11; i++ {
		l := fmt.Sprintf("Prefers the pasted line number %02d", i)
		lines = append(lines, l)
		fmt.Fprintf(&add, "- %s. Confidence: 0.50\n", l)
	}
	writeTaste(t, cfg, readFile(t, cfg.TastePath())+add.String())
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) || !HoldAcceptable(cfg) {
		t.Fatalf("setup: pending=%v acceptable=%v", HoldPending(cfg), HoldAcceptable(cfg))
	}

	var err error
	out := captureStderr(t, func() { err = AcceptHeldEdits(db, cfg, false) })
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("a cap-held hold must refuse without --yes and name it, got %v", err)
	}
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Fatalf("every line must be listed before the refusal, missing %q in %q", l, out)
		}
	}
	if !HoldPending(cfg) || countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers the pasted line%' AND valid_until IS NULL`) != 0 {
		t.Fatal("a refused accept must apply nothing and keep the hold")
	}

	if err := AcceptHeldEdits(db, cfg, true); err != nil {
		t.Fatalf("--yes must apply the cap-held hold: %v", err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers the pasted line%' AND valid_until IS NULL`); n != 11 {
		t.Fatalf("live pasted rows = %d, want 11", n)
	}
	if HoldPending(cfg) {
		t.Fatal("an accepted hold must be cleared")
	}
}

// An edit whose base checks out can still re-add text that left the profile
// (here the assistant re-adds a retired row). The accept lists that line and
// needs --yes for it.
func TestAcceptListsLinesThatLeftProfile(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
	seedStated(t, db, cfg, "General", "Prefers bravo.", 0.7)
	mustSync(t, db, cfg)
	if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE statement='Prefers bravo.'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	mustSync(t, db, cfg)
	appendBullet(t, cfg, "Testing", "Prefers bravo", 0.6)
	flagAssistantEdit(t, cfg.StatePath("model_edits.json"), cfg.TastePath())
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) || !HoldAcceptable(cfg) || HoldNeedsConfirm(cfg) {
		t.Fatalf("setup: want a verified-base hold, pending=%v acceptable=%v needsConfirm=%v", HoldPending(cfg), HoldAcceptable(cfg), HoldNeedsConfirm(cfg))
	}

	var err error
	out := captureStderr(t, func() { err = AcceptHeldEdits(db, cfg, false) })
	if err == nil {
		t.Fatal("re-adding a line that left the profile needs --yes even when the base checks out")
	}
	for _, want := range []string{"Prefers bravo", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must list the line and name the flag, missing %q in %q", want, err.Error())
		}
	}
	if !strings.Contains(out, "Prefers bravo") {
		t.Fatalf("the line must be listed on stderr, got %q", out)
	}
	if !HoldPending(cfg) || livePreferenceCount(t, db, "Prefers bravo") != 0 {
		t.Fatal("a refused accept must apply nothing and keep the hold")
	}

	if err := AcceptHeldEdits(db, cfg, true); err != nil {
		t.Fatal(err)
	}
	if livePreferenceCount(t, db, "Prefers bravo") != 1 {
		t.Fatal("--yes must bring the line back")
	}
}
