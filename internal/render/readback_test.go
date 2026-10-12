package render

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// seedStated inserts a live STATED preference directly, bypassing
// ApplySignal's own confidence math so the test can assert exact bullet text.
func seedStated(t *testing.T, db *sql.DB, cfg config.Config, category, statement string, confidence float64) {
	t.Helper()
	id := fmt.Sprintf("seed-%d", time.Now().UnixNano())
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES(?,?,?,?,?,?)`,
		id, category, statement, confidence, "STATED", "explicit_prefer"); err != nil {
		t.Fatal(err)
	}
}

// learnApplyStated records statement as an explicit_prefer signal (STATED
// tier) through the real learn path, for tests that need the accompanying
// create event rather than a bare row.
func learnApplyStated(db *sql.DB, cfg config.Config, category, statement string, confidence float64) (*learn.Preference, error) {
	return learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: statement, Category: category}, nil)
}

// newHomeCfg opens a real (non-memory) store under a fresh temp Home, so
// Sync/ReadBack's file-based machinery (state/render.lock, TASTE.base.md,
// readback.hold.json) runs for real instead of racing a package-global
// in-memory singleton across tests.
func newHomeCfg(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	storage.Close()
	cfg := config.Default()
	cfg.Home = t.TempDir()
	// config.Default's DBPath is a fixed relative default (".palate/…")
	// unless something re-derives it from Home, the way config.Resolve
	// does — done by hand here since this helper builds cfg directly.
	cfg.DBPath = filepath.Join(cfg.Home, "palate.db")
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(storage.Close)
	return db, cfg
}

func mustSync(t *testing.T, db *sql.DB, cfg config.Config) bool {
	t.Helper()
	wrote, err := Sync(db, cfg, cfg.TastePath())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return wrote
}

// appendBullet hand-edits TASTE.md the way an owner would: appends a
// category heading and one bullet, leaving the header untouched so its
// embedded sha no longer matches the new body.
func appendBullet(t *testing.T, cfg config.Config, heading, statement string, confidence float64) {
	t.Helper()
	b, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		t.Fatalf("read %s: %v", cfg.TastePath(), err)
	}
	addition := fmt.Sprintf("\n## %s\n\n- %s. Confidence: %.2f\n", heading, statement, confidence)
	if err := os.WriteFile(cfg.TastePath(), append(b, []byte(addition)...), 0644); err != nil {
		t.Fatalf("write %s: %v", cfg.TastePath(), err)
	}
}

func writeTaste(t *testing.T, cfg config.Config, content string) {
	t.Helper()
	if err := os.WriteFile(cfg.TastePath(), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", cfg.TastePath(), err)
	}
}

func livePreferenceCount(t *testing.T, db *sql.DB, statement string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE statement=? AND valid_until IS NULL`, statement).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func anyPreferenceCount(t *testing.T, db *sql.DB, statement string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE statement=?`, statement).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func provenanceOf(t *testing.T, db *sql.DB, statement string) string {
	t.Helper()
	var prov string
	if err := db.QueryRow(`SELECT provenance FROM preferences WHERE statement=?`, statement).Scan(&prov); err != nil {
		t.Fatal(err)
	}
	return prov
}

func isLive(t *testing.T, db *sql.DB, statement string) bool {
	t.Helper()
	var valid sql.NullString
	if err := db.QueryRow(`SELECT valid_until FROM preferences WHERE statement=?`, statement).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	return !valid.Valid
}

// TestReadBack exercises every branch of the read-back contract (phase-02
// req 5) against a real store and real TASTE.md/base files.
func TestReadBack(t *testing.T) {
	t.Run("clean when the header sha matches its own body", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "Testing", "Prefers table-driven tests", 0.8)
		mustSync(t, db, cfg)
		before, _ := os.ReadFile(cfg.TastePath())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(cfg.TastePath())
		if string(before) != string(after) {
			t.Fatal("a clean read-back must not touch the file")
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&n)
		if n != 1 {
			t.Fatalf("a clean read-back must not create rows, got %d preferences", n)
		}
	})

	t.Run("first run: no sha and no base is a clean no-op", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		writeTaste(t, cfg, "<!-- auto-maintained by palate after event #0; edit with `palate prefer` / `palate forget`, not by hand -->\n# Taste\n\n- Old-format bullet. Confidence: 0.50\n")
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if n := anyPreferenceCount(t, db, "Old-format bullet"); n != 0 {
			t.Fatalf("baseline mode must apply nothing, got %d rows", n)
		}
	})

	t.Run("added line becomes a STATED row tagged taste_edit", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg) // empty baseline: header + base with no bullets
		appendBullet(t, cfg, "Testing", "Prefers hand added spaces", 0.60)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if livePreferenceCount(t, db, "Prefers hand added spaces") != 1 {
			t.Fatal("expected exactly one live row for the added statement")
		}
		var tier string
		_ = db.QueryRow(`SELECT trust_tier FROM preferences WHERE statement=?`, "Prefers hand added spaces").Scan(&tier)
		if tier != "STATED" {
			t.Fatalf("hand edit must land STATED, got %q", tier)
		}
		if !strings.Contains(provenanceOf(t, db, "Prefers hand added spaces"), "taste_edit") {
			t.Fatal("hand edit must carry the taste_edit provenance tag")
		}
	})

	t.Run("a line matching a since-retired row's still-in-base text is not re-added", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers spaces", 0.7)
		mustSync(t, db, cfg)
		// Retire it out-of-band (a forget or a consolidation elsewhere) —
		// the file itself is not touched yet, so it still shows the line.
		if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE statement='Prefers spaces'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		appendBullet(t, cfg, "Testing", "Prefers hand added spaces", 0.60)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if n := anyPreferenceCount(t, db, "Prefers spaces"); n != 1 {
			t.Fatalf("an unchanged base line must never be re-added, got %d rows", n)
		}
		if livePreferenceCount(t, db, "Prefers hand added spaces") != 1 {
			t.Fatal("the genuinely new line must still be applied")
		}
	})

	t.Run("a missing base holds and applies nothing", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		appendBullet(t, cfg, "Testing", "Prefers a missing base", 0.6)
		if err := os.Remove(cfg.StatePath("TASTE.base.md")); err != nil {
			t.Fatal(err)
		}
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("a missing base must hold, not apply")
		}
		if n := anyPreferenceCount(t, db, "Prefers a missing base"); n != 0 {
			t.Fatalf("nothing must be applied against an unknown base, got %d rows", n)
		}
	})

	t.Run("a base mismatch holds and applies nothing", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		appendBullet(t, cfg, "Testing", "Prefers a mismatched base", 0.6)
		if err := os.WriteFile(cfg.StatePath("TASTE.base.md"), []byte("garbage, not the real base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("a base mismatch must hold, not apply")
		}
		if n := anyPreferenceCount(t, db, "Prefers a mismatched base"); n != 0 {
			t.Fatalf("nothing must be applied against a mismatched base, got %d rows", n)
		}
	})

	t.Run("a reworded line inserts the new text and forgets the old", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers spaces", 0.7)
		// A second, untouched row keeps the implied deletion at 1-of-2 (50%,
		// not the > 50% mass-deletion threshold) — rewording a category's
		// only line would otherwise look identical to deleting it outright.
		seedStated(t, db, cfg, "General", "Prefers early returns", 0.6)
		mustSync(t, db, cfg)
		b, err := os.ReadFile(cfg.TastePath())
		if err != nil {
			t.Fatal(err)
		}
		reworded := strings.Replace(string(b), "Prefers spaces", "Prefers double spaces", 1)
		writeTaste(t, cfg, reworded)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if livePreferenceCount(t, db, "Prefers double spaces") != 1 {
			t.Fatal("the reworded text must land as a new STATED row")
		}
		if isLive(t, db, "Prefers spaces") {
			t.Fatal("the old wording must soft-retire")
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='taste_edit_forget' AND payload LIKE '%deleted from TASTE.md by hand%'`).Scan(&n)
		if n != 1 {
			t.Fatalf("expected one taste_edit_forget event, got %d", n)
		}
	})

	t.Run("a deleted line soft-retires and stays restorable", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers tabs", 0.7)
		seedStated(t, db, cfg, "General", "Prefers early returns", 0.6)
		mustSync(t, db, cfg)
		b, err := os.ReadFile(cfg.TastePath())
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "Prefers tabs") {
				continue
			}
			kept = append(kept, line)
		}
		writeTaste(t, cfg, strings.Join(kept, "\n"))
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if isLive(t, db, "Prefers tabs") {
			t.Fatal("the deleted line must soft-retire")
		}
		if !isLive(t, db, "Prefers early returns") {
			t.Fatal("the kept line must stay live")
		}
		var id string
		_ = db.QueryRow(`SELECT id FROM preferences WHERE statement='Prefers tabs'`).Scan(&id)
		if id == "" {
			t.Fatal("a soft-retired row must still exist to restore")
		}
	})

	t.Run("more than half deleted holds the deletions, still applies additions", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers keep", 0.7)
		seedStated(t, db, cfg, "General", "Prefers drop one", 0.6)
		seedStated(t, db, cfg, "General", "Prefers drop two", 0.6)
		mustSync(t, db, cfg)
		b, err := os.ReadFile(cfg.TastePath())
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "Prefers drop one") || strings.Contains(line, "Prefers drop two") {
				continue
			}
			kept = append(kept, line)
		}
		writeTaste(t, cfg, strings.Join(kept, "\n"))
		appendBullet(t, cfg, "Testing", "Prefers a fresh addition", 0.5)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("deleting more than half the base must hold")
		}
		if !isLive(t, db, "Prefers drop one") || !isLive(t, db, "Prefers drop two") {
			t.Fatal("held deletions must not be applied")
		}
		if livePreferenceCount(t, db, "Prefers a fresh addition") != 1 {
			t.Fatal("additions still apply even while deletions are held")
		}
	})

	t.Run("a Vietnamese line round-trips unchanged", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		appendBullet(t, cfg, "Testing", "Ưu tiên viết test theo kiểu bảng", 0.70)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if livePreferenceCount(t, db, "Ưu tiên viết test theo kiểu bảng") != 1 {
			t.Fatal("the Vietnamese statement must round-trip byte-for-byte")
		}
	})
}

// TestReadBackHoldsRenderLock: a lock held elsewhere makes both ReadBack and
// Sync skip without writing; releasing it lets the same edit through exactly
// once, however many times it is read back before the next Sync advances
// the base (dedup-by-statement makes a repeat a reinforce, not a twin).
func TestReadBackHoldsRenderLock(t *testing.T) {
	db, cfg := newHomeCfg(t)
	mustSync(t, db, cfg)
	appendBullet(t, cfg, "Testing", "Prefers locked reads", 0.6)

	release, ok := distill.AcquireLearnLock(cfg.StatePath("render.lock"))
	if !ok {
		t.Fatal("expected to acquire the lock myself")
	}

	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if n := anyPreferenceCount(t, db, "Prefers locked reads"); n != 0 {
		t.Fatalf("ReadBack must skip while the lock is held, got %d rows", n)
	}
	if wrote, err := Sync(db, cfg, cfg.TastePath()); err != nil || wrote {
		t.Fatalf("Sync must skip while the lock is held: wrote=%v err=%v", wrote, err)
	}

	release()

	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if n := anyPreferenceCount(t, db, "Prefers locked reads"); n != 1 {
		t.Fatalf("two sequential read-backs of the same edit must ingest it exactly once, got %d rows", n)
	}
}

// TestNoShaEditWithBaseIsHeldNotLost: a base exists, but the on-disk file
// has been replaced with an older-format (no sha) edit — Sync must hold it and
// never overwrite it. Such a hold is discard-only: --accept-edits refuses it
// (the old binary's file is a full dump, not a hand edit), and discarding it
// lets the next Sync render the new format.
func TestNoShaEditWithBaseIsHeldNotLost(t *testing.T) {
	db, cfg := newHomeCfg(t)
	mustSync(t, db, cfg)
	oldFormat := "<!-- auto-maintained by palate after event #1; edit with `palate prefer` / `palate forget`, not by hand -->\n# Taste\n\n## Testing\n\n- Prefers legacy-format edit. Confidence: 0.55\n\n"
	writeTaste(t, cfg, oldFormat)

	mustSync(t, db, cfg)
	if !HoldPending(cfg) {
		t.Fatal("a no-sha edit against a known base must hold")
	}
	if HoldAcceptable(cfg) {
		t.Fatal("an old-format hold must not be acceptable")
	}
	onDisk, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != oldFormat {
		t.Fatal("Sync must not overwrite the file while a hold is pending")
	}
	if n := anyPreferenceCount(t, db, "Prefers legacy-format edit"); n != 0 {
		t.Fatal("nothing should be applied yet")
	}

	if err := AcceptHeldEdits(db, cfg, false); err == nil {
		t.Fatal("AcceptHeldEdits must refuse an old-format hold")
	}
	if !HoldPending(cfg) || anyPreferenceCount(t, db, "Prefers legacy-format edit") != 0 {
		t.Fatal("a refused accept must leave the hold in place and apply nothing")
	}
	if err := DiscardHold(cfg); err != nil {
		t.Fatal(err)
	}
	mustSync(t, db, cfg)
	if HoldPending(cfg) || strings.Contains(readFile(t, cfg.TastePath()), "Prefers legacy-format edit") {
		t.Fatal("discard must clear the hold and the next Sync must render the profile without the old-format line")
	}
}

// TestHoldBlocksOverwrite: with a hold pending, repeated Sync calls keep
// writing profile membership events but never touch TASTE.md's bytes; a
// plain discard (DiscardHold) drops the held edit and lets Sync re-render.
func TestHoldBlocksOverwrite(t *testing.T) {
	db, cfg := newHomeCfg(t)
	mustSync(t, db, cfg)
	appendBullet(t, cfg, "Testing", "Prefers held content", 0.6)
	if err := os.Remove(cfg.StatePath("TASTE.base.md")); err != nil {
		t.Fatal(err)
	}
	mustSync(t, db, cfg) // triggers the hold (base missing)
	if !HoldPending(cfg) {
		t.Fatal("expected a hold")
	}
	before := readFile(t, cfg.TastePath())

	for i := 0; i < 3; i++ {
		if _, err := learnApplyStated(db, cfg, "General", fmt.Sprintf("Prefers new row %d", i), 0.5); err != nil {
			t.Fatal(err)
		}
		mustSync(t, db, cfg)
	}
	if after := readFile(t, cfg.TastePath()); before != after {
		t.Fatal("TASTE.md must stay unchanged while a hold is pending")
	}
	var creates int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='create'`).Scan(&creates)
	if creates < 3 {
		t.Fatalf("profile events must keep being written while held, got %d creates", creates)
	}

	if err := DiscardHold(cfg); err != nil {
		t.Fatal(err)
	}
	if HoldPending(cfg) {
		t.Fatal("discard must clear the hold")
	}
	mustSync(t, db, cfg)
	if strings.Contains(readFile(t, cfg.TastePath()), "Prefers held content") {
		t.Fatal("discard must drop the held hand edit, not apply it")
	}
}

// TestAssistantEditOfTasteIsHeld: TASTE.md's own path present in
// state/model_edits.json means the assistant, not the owner, wrote the
// bytes on disk — read-back must hold rather than mint a STATED row from it
// (red-team H4, owner default Q1).
func TestAssistantEditOfTasteIsHeld(t *testing.T) {
	db, cfg := newHomeCfg(t)
	mustSync(t, db, cfg)
	appendBullet(t, cfg, "Testing", "Prefers an assistant edit", 0.6)
	if err := os.WriteFile(cfg.StatePath("model_edits.json"), []byte(fmt.Sprintf(`{%q:{"hash":"deadbeef"}}`, cfg.TastePath())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) {
		t.Fatal("an assistant-edited path must hold")
	}
	if n := anyPreferenceCount(t, db, "Prefers an assistant edit"); n != 0 {
		t.Fatalf("nothing must be applied from an assistant edit, got %d rows", n)
	}
	if err := AcceptHeldEdits(db, cfg, false); err != nil {
		t.Fatal(err)
	}
	if livePreferenceCount(t, db, "Prefers an assistant edit") != 1 {
		t.Fatal("--accept-edits must apply an assistant-edit hold once the owner approves it")
	}
}

// TestMassAdditionHeld: more than maxHandEditAdditions new lines in one
// read-back holds the whole batch; exactly the limit still applies.
func TestMassAdditionHeld(t *testing.T) {
	t.Run("11 additions hold", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		b, _ := os.ReadFile(cfg.TastePath())
		var extra strings.Builder
		extra.WriteString("\n## Testing\n\n")
		for i := 0; i < 11; i++ {
			fmt.Fprintf(&extra, "- Prefers mass added line %d. Confidence: 0.50\n", i)
		}
		writeTaste(t, cfg, string(b)+extra.String())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("11 additions must hold")
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers mass added line%'`).Scan(&n)
		if n != 0 {
			t.Fatalf("a held batch must apply nothing, got %d rows", n)
		}
	})

	t.Run("10 additions apply", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		b, _ := os.ReadFile(cfg.TastePath())
		var extra strings.Builder
		extra.WriteString("\n## Testing\n\n")
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&extra, "- Prefers mass added line %d. Confidence: 0.50\n", i)
		}
		writeTaste(t, cfg, string(b)+extra.String())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if HoldPending(cfg) {
			t.Fatal("exactly 10 additions must not hold")
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers mass added line%'`).Scan(&n)
		if n != 10 {
			t.Fatalf("expected all 10 additions applied, got %d", n)
		}
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
