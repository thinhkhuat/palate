package render

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every statement in this file ends with a period, as every live statement on
// the owner's store does; TASTE.md drops exactly that period when it renders,
// which is the shape the original read-back and seed tests never covered.

// removeLine returns content without any line containing needle.
func removeLine(content, needle string) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func countQuery(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func flagAssistantEdit(t *testing.T, cfgPath, tastePath string) {
	t.Helper()
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`{%q:{"hash":"deadbeef"}}`, tastePath)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadBackMatchesTrailingPeriod(t *testing.T) {
	t.Run("deleting a period-terminated line retires its row and the line stays gone", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers tabs.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers early returns.", 0.6)
		mustSync(t, db, cfg)
		writeTaste(t, cfg, removeLine(readFile(t, cfg.TastePath()), "Prefers tabs"))
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		mustSync(t, db, cfg)
		if isLive(t, db, "Prefers tabs.") {
			t.Fatal("the deleted line's row must soft-retire")
		}
		if !isLive(t, db, "Prefers early returns.") {
			t.Fatal("the kept line's row must stay live")
		}
		if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='taste_edit_forget'`); n != 1 {
			t.Fatalf("taste_edit_forget events = %d, want 1", n)
		}
		if strings.Contains(readFile(t, cfg.TastePath()), "Prefers tabs") {
			t.Fatal("the deleted line must not come back on the next render")
		}
		if HoldPending(cfg) {
			t.Fatal("a plain deletion must not hold")
		}
	})

	t.Run("rewording a period-terminated line retires the old row, no twin", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers tabs.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers early returns.", 0.6)
		mustSync(t, db, cfg)
		writeTaste(t, cfg, strings.Replace(readFile(t, cfg.TastePath()), "Prefers tabs", "Prefers hard tabs", 1))
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if isLive(t, db, "Prefers tabs.") {
			t.Fatal("the old wording must soft-retire")
		}
		if n := livePreferenceCount(t, db, "Prefers hard tabs"); n != 1 {
			t.Fatalf("live rows for the new wording = %d, want 1", n)
		}
		if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE valid_until IS NULL`); n != 2 {
			t.Fatalf("live rows = %d, want 2 (one reworded, one untouched)", n)
		}
	})

	t.Run("typing an existing candidate's text reinforces that row instead of adding a twin", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES('cand','General','Prefers a quiet candidate.',0.5,'DISTILLED','llm_distilled')`); err != nil {
			t.Fatal(err)
		}
		mustSync(t, db, cfg)
		appendBullet(t, cfg, "General", "Prefers a quiet candidate", 0.6)
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE valid_until IS NULL AND statement LIKE 'Prefers a quiet candidate%'`); n != 1 {
			t.Fatalf("live rows for the typed text = %d, want 1 (the existing row, not a twin)", n)
		}
	})
}

func TestFirstRunSeedMatchesLiveTaste(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers tabs.", 0.7)
	seedStated(t, db, cfg, "General", "Prefers early returns.", 0.6)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES('old','General','Remembered once.',0.5,'DISTILLED','llm_distilled')`); err != nil {
		t.Fatal(err)
	}
	// The pre-switch-over file: every live row, the period dropped by the renderer.
	writeTaste(t, cfg, "<!-- auto-maintained by palate after event #3 -->\n# Taste\n\n## General\n\n"+
		"- Prefers tabs. Confidence: 0.70\n- Prefers early returns. Confidence: 0.60\n- Remembered once. Confidence: 0.50\n- A line no row backs. Confidence: 0.50\n\n")

	got, err := seedMembershipFromDisk(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("seed recognised %d members, want 3 (the three lines that have a live row)", len(got))
	}
	if !got["old"] {
		t.Fatal("the candidate row 'old' must be recognised from its on-disk line")
	}

	// End to end: the first Sync records the old line that leaves the profile.
	mustSync(t, db, cfg)
	if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='profile_cut' AND json_extract(payload,'$.id')='old' AND json_extract(payload,'$.seed')=1`); n != 1 {
		t.Fatalf("seed profile_cut events for the old line = %d, want 1", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='profile_add'`); n != 0 {
		t.Fatalf("profile_add events = %d, want 0 (both STATED rows were already on disk)", n)
	}
}

// oldFormatDump leaves TASTE.md the way an old binary renders it: every live
// row, old header, no sha.
func oldFormatDump(t *testing.T, db *sql.DB, cfgWrite func(string)) {
	t.Helper()
	var b strings.Builder
	b.WriteString("<!-- auto-maintained by palate after event #9; edit with `palate prefer` / `palate forget`, not by hand -->\n# Taste\n\n## General\n\n")
	rows, err := db.Query(`SELECT statement, confidence FROM preferences WHERE valid_until IS NULL ORDER BY statement`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		var c float64
		if err := rows.Scan(&s, &c); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "- %s. Confidence: %.2f\n", strings.TrimSuffix(s, "."), c)
	}
	rows.Close()
	cfgWrite(b.String())
}

func TestAcceptOldFormatHoldIsDiscardOnly(t *testing.T) {
	db, cfg := newHomeCfg(t)
	for i := 0; i < 3; i++ {
		seedStated(t, db, cfg, "General", fmt.Sprintf("Prefers stated thing %d.", i), 0.9)
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 15; i++ {
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, trust_tier, source) VALUES(?,?,?,?,?,?)`,
			fmt.Sprintf("d%02d", i), "General", fmt.Sprintf("Distilled candidate %02d.", i), 0.5, "DISTILLED", "llm_distilled"); err != nil {
			t.Fatal(err)
		}
	}
	mustSync(t, db, cfg)
	oldFormatDump(t, db, func(s string) { writeTaste(t, cfg, s) })
	mustSync(t, db, cfg)
	if !HoldPending(cfg) {
		t.Fatal("setup: an old-format rewrite must hold")
	}
	live := func() int { return countQuery(t, db, `SELECT count(*) FROM preferences WHERE valid_until IS NULL`) }
	stated := func() int {
		return countQuery(t, db, `SELECT count(*) FROM preferences WHERE valid_until IS NULL AND trust_tier='STATED'`)
	}
	if live() != 18 || stated() != 3 {
		t.Fatalf("setup: live=%d stated=%d, want 18 and 3", live(), stated())
	}

	err := AcceptHeldEdits(db, cfg, false)
	if err == nil {
		t.Fatal("accepting an old-format hold must be refused")
	}
	if !strings.Contains(err.Error(), "palate taste") || strings.Contains(err.Error(), "--accept-edits") {
		t.Fatalf("the refusal must name the discard command and nothing to accept, got %q", err.Error())
	}
	if !HoldPending(cfg) {
		t.Fatal("a refused accept must leave the hold in place")
	}
	if live() != 18 || stated() != 3 {
		t.Fatalf("a refused accept applied something: live=%d stated=%d, want 18 and 3", live(), stated())
	}

	if err := DiscardHold(cfg); err != nil {
		t.Fatal(err)
	}
	mustSync(t, db, cfg)
	if HoldPending(cfg) || !strings.Contains(readFile(t, cfg.TastePath()), "sha256=") {
		t.Fatal("discard must clear the hold and the next Sync must render the new format")
	}
}

func TestAcceptEditsUsesBaseProfile(t *testing.T) {
	retire := func(t *testing.T, db *sql.DB, statement string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE statement=?`, time.Now().UTC().Format(time.RFC3339), statement); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an exact-base hold applies against the base it was written from", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers bravo.", 0.7)
		mustSync(t, db, cfg)
		appendBullet(t, cfg, "General", "Prefers delta", 0.9)
		flagAssistantEdit(t, cfg.StatePath("model_edits.json"), cfg.TastePath())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: the assistant edit must hold")
		}
		// While held: a learner adds charlie, the dream pass retires bravo, and
		// Sync keeps moving the base.
		seedStated(t, db, cfg, "General", "Prefers charlie.", 0.7)
		retire(t, db, "Prefers bravo.")
		mustSync(t, db, cfg)

		if err := AcceptHeldEdits(db, cfg, false); err != nil {
			t.Fatal(err)
		}
		if n := livePreferenceCount(t, db, "Prefers delta"); n != 1 {
			t.Fatalf("live delta rows = %d, want 1", n)
		}
		if !isLive(t, db, "Prefers charlie.") {
			t.Fatal("charlie was never in the held file and the owner never deleted it, but accept retired it")
		}
		if n := livePreferenceCount(t, db, "Prefers bravo"); n != 0 {
			t.Fatalf("live rows for bravo's text = %d, want 0: accept resurrected a row retired after the hold", n)
		}
		if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='taste_edit_forget'`); n != 0 {
			t.Fatalf("taste_edit_forget events = %d, want 0", n)
		}
	})

	t.Run("a stale editor buffer adds its new line and deletes nothing", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers bravo.", 0.7)
		mustSync(t, db, cfg)
		editorBuffer := readFile(t, cfg.TastePath())
		seedStated(t, db, cfg, "General", "Prefers charlie.", 0.7)
		mustSync(t, db, cfg)
		writeTaste(t, cfg, editorBuffer+"- Prefers delta. Confidence: 0.90\n")
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: a stale-buffer save must hold")
		}
		retire(t, db, "Prefers bravo.")
		mustSync(t, db, cfg)

		if err := AcceptHeldEdits(db, cfg, false); err != nil {
			t.Fatal(err)
		}
		if n := livePreferenceCount(t, db, "Prefers delta"); n != 1 {
			t.Fatalf("live delta rows = %d, want 1", n)
		}
		if !isLive(t, db, "Prefers charlie.") {
			t.Fatal("charlie was missing from the stale buffer only; accept must not retire it")
		}
		if n := livePreferenceCount(t, db, "Prefers bravo"); n != 0 {
			t.Fatalf("live rows for bravo's text = %d, want 0", n)
		}
		if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='taste_edit_forget'`); n != 0 {
			t.Fatalf("taste_edit_forget events = %d, want 0", n)
		}
	})

	t.Run("accept keeps the line cap for an assistant-edit hold", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		var extra strings.Builder
		extra.WriteString("\n## Testing\n\n")
		for i := 0; i < 11; i++ {
			fmt.Fprintf(&extra, "- Prefers assistant added line %d. Confidence: 0.50\n", i)
		}
		writeTaste(t, cfg, readFile(t, cfg.TastePath())+extra.String())
		flagAssistantEdit(t, cfg.StatePath("model_edits.json"), cfg.TastePath())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: the assistant edit must hold")
		}
		if err := AcceptHeldEdits(db, cfg, false); err == nil {
			t.Fatal("11 added lines from an assistant edit exceed the cap and must be refused on accept")
		}
		if !HoldPending(cfg) {
			t.Fatal("a refused accept must leave the hold in place")
		}
		if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers assistant added line%'`); n != 0 {
			t.Fatalf("a refused accept applied %d rows", n)
		}
	})

	t.Run("accept of the cap hold itself applies all 11 lines", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		mustSync(t, db, cfg)
		var extra strings.Builder
		extra.WriteString("\n## Testing\n\n")
		for i := 0; i < 11; i++ {
			fmt.Fprintf(&extra, "- Prefers mass added line %d. Confidence: 0.50\n", i)
		}
		writeTaste(t, cfg, readFile(t, cfg.TastePath())+extra.String())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: 11 additions must hold")
		}
		if err := AcceptHeldEdits(db, cfg, true); err != nil {
			t.Fatal(err)
		}
		if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers mass added line%' AND valid_until IS NULL`); n != 11 {
			t.Fatalf("live rows after accepting the cap hold = %d, want 11", n)
		}
	})

	t.Run("accept keeps the mass-deletion guard for an assistant-edit hold", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers keep.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers drop one.", 0.6)
		seedStated(t, db, cfg, "General", "Prefers drop two.", 0.6)
		mustSync(t, db, cfg)
		edited := removeLine(removeLine(readFile(t, cfg.TastePath()), "Prefers drop one"), "Prefers drop two")
		writeTaste(t, cfg, edited)
		flagAssistantEdit(t, cfg.StatePath("model_edits.json"), cfg.TastePath())
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: the assistant edit must hold")
		}
		if err := AcceptHeldEdits(db, cfg, false); err == nil {
			t.Fatal("deleting 2 of 3 lines via an assistant edit exceeds the mass-deletion guard and must be refused on accept")
		}
		if !isLive(t, db, "Prefers drop one.") || !isLive(t, db, "Prefers drop two.") {
			t.Fatal("a refused accept must retire nothing")
		}
	})

	t.Run("accept of the mass-deletion hold itself applies the deletions", func(t *testing.T) {
		db, cfg := newHomeCfg(t)
		seedStated(t, db, cfg, "General", "Prefers keep.", 0.7)
		seedStated(t, db, cfg, "General", "Prefers drop one.", 0.6)
		seedStated(t, db, cfg, "General", "Prefers drop two.", 0.6)
		mustSync(t, db, cfg)
		writeTaste(t, cfg, removeLine(removeLine(readFile(t, cfg.TastePath()), "Prefers drop one"), "Prefers drop two"))
		if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
			t.Fatal(err)
		}
		if !HoldPending(cfg) {
			t.Fatal("setup: deleting more than half must hold")
		}
		if err := AcceptHeldEdits(db, cfg, false); err != nil {
			t.Fatal(err)
		}
		if isLive(t, db, "Prefers drop one.") || isLive(t, db, "Prefers drop two.") || !isLive(t, db, "Prefers keep.") {
			t.Fatal("accepting the mass-deletion hold must retire exactly the two deleted lines")
		}
	})
}

// Reimport (`palate pull`) skips a line whose statement a live row already
// has; with period-terminated statements that was never true.
func TestReimportMatchesTrailingPeriod(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers tabs.", 0.7)
	path := cfg.Home + "/import.md"
	if err := os.WriteFile(path, []byte("# Taste\n\n## General\n\n- Prefers tabs. Confidence: 0.50\n"), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Reimport(db, cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("Reimport imported %d line(s), want 0: the statement already has a live row", n)
	}
	if got := countQuery(t, db, `SELECT count(*) FROM preferences`); got != 1 {
		t.Fatalf("preferences = %d, want 1", got)
	}
}

// A hold file that cannot be parsed is discard-only, so the command the
// notice offers (`palate taste` -> DiscardHold) must be able to clear it.
func TestDiscardCorruptHold(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers tabs.", 0.7)
	mustSync(t, db, cfg)
	if err := os.WriteFile(cfg.StatePath("readback.hold.json"), []byte(`{"held_path": "oops`), 0600); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) || HoldAcceptable(cfg) {
		t.Fatalf("setup: pending=%v acceptable=%v, want true and false", HoldPending(cfg), HoldAcceptable(cfg))
	}
	if err := DiscardHold(cfg); err != nil {
		t.Fatal(err)
	}
	if HoldPending(cfg) {
		t.Fatal("discarding a corrupt hold must remove the hold file")
	}
	if got := stateFiles(t, cfg, "readback.hold.discarded-*.json"); len(got) != 1 || readFile(t, got[0]) != `{"held_path": "oops` {
		t.Fatalf("the corrupt hold file must be archived, got %v", got)
	}
	mustSync(t, db, cfg)
	if !strings.Contains(readFile(t, cfg.TastePath()), "Prefers tabs") {
		t.Fatal("after the discard the next Sync must render the profile")
	}
}

// Round 2.

func stateFiles(t *testing.T, cfg interface{ StatePath(string) string }, glob string) []string {
	t.Helper()
	m, err := filepath.Glob(cfg.StatePath(glob))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStaleBufferAcceptConfirmsRestoredLines(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
	seedStated(t, db, cfg, "General", "Prefers bravo.", 0.7)
	mustSync(t, db, cfg)
	editorBuffer := readFile(t, cfg.TastePath())
	// The dream pass retires bravo and Sync re-renders before the owner saves.
	if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE statement='Prefers bravo.'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	mustSync(t, db, cfg)
	writeTaste(t, cfg, editorBuffer+"- Prefers delta. Confidence: 0.90\n")
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) || !HoldAcceptable(cfg) {
		t.Fatalf("setup: pending=%v acceptable=%v, want true and true", HoldPending(cfg), HoldAcceptable(cfg))
	}

	err := AcceptHeldEdits(db, cfg, false)
	if err == nil {
		t.Fatal("accepting a stale buffer that would bring back a retired line needs the explicit confirmation")
	}
	for _, want := range []string{"Prefers bravo", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must list the line and name the flag, missing %q in %q", want, err.Error())
		}
	}
	if !HoldPending(cfg) {
		t.Fatal("a refused accept must leave the hold in place")
	}
	if n := livePreferenceCount(t, db, "Prefers delta"); n != 0 {
		t.Fatalf("a refused accept applied %d row(s)", n)
	}
	if n := livePreferenceCount(t, db, "Prefers bravo"); n != 0 {
		t.Fatalf("a refused accept restored bravo (%d live rows)", n)
	}

	if err := AcceptHeldEdits(db, cfg, true); err != nil {
		t.Fatal(err)
	}
	if n := livePreferenceCount(t, db, "Prefers delta"); n != 1 {
		t.Fatalf("live delta rows = %d, want 1", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement='Prefers bravo' AND valid_until IS NULL AND trust_tier='STATED'`); n != 1 {
		t.Fatalf("confirmed accept must bring bravo back as one STATED row, got %d", n)
	}
}

func TestHeaderRemovedEditAcceptsAdditionsOnly(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
	seedStated(t, db, cfg, "General", "Prefers bravo.", 0.7)
	mustSync(t, db, cfg)
	noHeader := strings.SplitN(readFile(t, cfg.TastePath()), "\n", 2)[1]
	writeTaste(t, cfg, removeLine(noHeader, "Prefers bravo")+"- Prefers delta. Confidence: 0.90\n")
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) {
		t.Fatal("setup: a header-less edit must hold")
	}
	if !HoldAcceptable(cfg) {
		t.Fatal("a file that only lost its header line is not an old-format file: it must be acceptable")
	}
	if err := AcceptHeldEdits(db, cfg, false); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if n := livePreferenceCount(t, db, "Prefers delta"); n != 1 {
		t.Fatalf("live delta rows = %d, want 1", n)
	}
	if !isLive(t, db, "Prefers bravo.") {
		t.Fatal("the deleted line must not retire anything: the base cannot be verified")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='taste_edit_forget'`); n != 0 {
		t.Fatalf("taste_edit_forget events = %d, want 0", n)
	}
}

func TestDiscardArchivesHeldCopy(t *testing.T) {
	db, cfg := newHomeCfg(t)
	mustSync(t, db, cfg)
	var extra strings.Builder
	extra.WriteString("\n## Testing\n\n")
	for i := 0; i < 11; i++ {
		fmt.Fprintf(&extra, "- Prefers my own line %d. Confidence: 0.50\n", i)
	}
	writeTaste(t, cfg, readFile(t, cfg.TastePath())+extra.String())
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	h, err := readHold(cfg)
	if err != nil {
		t.Fatal(err)
	}
	held := readFile(t, h.HeldPath)
	if err := DiscardHold(cfg); err != nil {
		t.Fatal(err)
	}
	if HoldPending(cfg) {
		t.Fatal("discard must clear the hold")
	}
	got := stateFiles(t, cfg, "TASTE.discarded-*.md")
	if len(got) != 1 {
		t.Fatalf("archived copies = %v, want exactly one TASTE.discarded-*.md", got)
	}
	if readFile(t, got[0]) != held {
		t.Fatal("the archived copy must be byte-identical to the held edit")
	}
}

func TestResolvedAssistantHoldDoesNotTaintNextEdit(t *testing.T) {
	for _, resolve := range []string{"discard", "accept"} {
		t.Run(resolve, func(t *testing.T) {
			db, cfg := newHomeCfg(t)
			mustSync(t, db, cfg)
			appendBullet(t, cfg, "General", "Prefers the assistants line", 0.9)
			flagAssistantEdit(t, cfg.StatePath("model_edits.json"), cfg.TastePath())
			if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
				t.Fatal(err)
			}
			if !HoldPending(cfg) {
				t.Fatal("setup: the assistant edit must hold")
			}
			if resolve == "discard" {
				if err := DiscardHold(cfg); err != nil {
					t.Fatal(err)
				}
			} else if err := AcceptHeldEdits(db, cfg, false); err != nil {
				t.Fatal(err)
			}
			mustSync(t, db, cfg)
			appendBullet(t, cfg, "General", "Prefers the owners own line", 0.9)
			if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
				t.Fatal(err)
			}
			if HoldPending(cfg) {
				t.Fatal("the owner's next edit was held as the assistant's because of a resolved hold's leftover model_edits entry")
			}
			if n := livePreferenceCount(t, db, "Prefers the owners own line"); n != 1 {
				t.Fatalf("live rows for the owner's line = %d, want 1", n)
			}
		})
	}
}
