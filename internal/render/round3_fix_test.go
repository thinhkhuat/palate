package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	return <-done
}

// A row the learner rewords (a `refine` event) leaves its old wording in the
// owner's editor buffer. Accepting that buffer must list the old wording like
// it does for a dream-pass rewrite, and need --yes, instead of filing it as a
// new STATED twin of the reworded row.
func TestStaleBufferListsLearnerRewordings(t *testing.T) {
	db, cfg := newHomeCfg(t)
	seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
	seedDistilledID(t, db, "B", "General", "Prefers the bravo layout.", 0.8)
	for _, s := range []string{"s1", "s2"} {
		if err := storage.AppendEvent(db, "create", fmt.Sprintf(`{"id":"B","session":%q}`, s)); err != nil {
			t.Fatal(err)
		}
	}
	mustSync(t, db, cfg)
	editorBuffer := readFile(t, cfg.TastePath())
	if !strings.Contains(editorBuffer, "Prefers the bravo layout") {
		t.Fatalf("setup: row B is not in TASTE.md:\n%s", editorBuffer)
	}
	if _, err := learn.ApplyJudged(db, cfg, learn.Signal{Type: learn.SignalLLMDistilled},
		learn.Assessment{Statement: "Prefers bravo layout.", Relation: learn.RelationRefines, TargetID: "B", Confidence: 0.8, By: "test"}); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM events WHERE event_type='refine'`); n != 1 {
		t.Fatalf("setup: refine events = %d, want 1", n)
	}
	mustSync(t, db, cfg)
	writeTaste(t, cfg, editorBuffer+"- Prefers delta. Confidence: 0.90\n")
	if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !HoldPending(cfg) || !HoldAcceptable(cfg) {
		t.Fatalf("setup: pending=%v acceptable=%v, want true and true", HoldPending(cfg), HoldAcceptable(cfg))
	}

	var err error
	captureStderr(t, func() { err = AcceptHeldEdits(db, cfg, false) })
	if err == nil {
		t.Fatal("accepting a buffer that holds a reworded row's old wording needs --yes")
	}
	for _, want := range []string{"Prefers the bravo layout", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must list the old wording and name the flag, missing %q in %q", want, err.Error())
		}
	}
	if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers the bravo layout%' AND valid_until IS NULL`); n != 0 {
		t.Fatalf("a refused accept filed the old wording (%d live rows)", n)
	}
	if !HoldPending(cfg) {
		t.Fatal("a refused accept must leave the hold in place")
	}
}

// Edits saved to TASTE.md after a hold (the hold freezes Sync, so they live
// only there) must be archived before accept or discard rewrites the file.
func TestEditsSavedDuringHoldArchived(t *testing.T) {
	for _, resolve := range []string{"discard", "accept"} {
		t.Run(resolve, func(t *testing.T) {
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
			if !HoldPending(cfg) {
				t.Fatal("setup: eleven added lines must hold")
			}
			later := "- Prefers my later unique correction. Confidence: 0.90\n"
			writeTaste(t, cfg, readFile(t, cfg.TastePath())+later)

			var err error
			out := captureStderr(t, func() {
				if resolve == "discard" {
					err = DiscardHold(cfg)
				} else {
					err = AcceptHeldEdits(db, cfg, true) // eleven lines: over the cap, so --yes
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			var found []string
			_ = filepath.Walk(cfg.Home, func(p string, info os.FileInfo, werr error) error {
				if werr == nil && !info.IsDir() && strings.Contains(readFile(t, p), "my later unique correction") && !strings.HasSuffix(p, ".db") && !strings.Contains(p, "-wal") && !strings.Contains(p, "-shm") {
					found = append(found, p)
				}
				return nil
			})
			if len(found) == 0 {
				t.Fatal("the edit saved after the hold is gone from disk: it must be archived under state/")
			}
			if got := stateFiles(t, cfg, "TASTE.after-hold-*.md"); len(got) != 1 || !strings.Contains(readFile(t, got[0]), "my later unique correction") {
				t.Fatalf("archived later copies = %v, want one TASTE.after-hold-*.md holding the edit", got)
			}
			if !strings.Contains(out, "TASTE.after-hold-") {
				t.Fatalf("the archive path must be printed, stderr = %q", out)
			}
		})
	}
}

// A held edit over the 10-line cap is listed in full. Without --yes it is
// refused and the message names --yes; with --yes it is applied.
func TestYesOverridesLineCapAfterListing(t *testing.T) {
	eleven := func() (body string, lines []string) {
		var b strings.Builder
		b.WriteString("\n## Testing\n\n")
		for i := 0; i < 11; i++ {
			l := fmt.Sprintf("Prefers the over-cap line number %02d", i)
			lines = append(lines, l)
			fmt.Fprintf(&b, "- %s. Confidence: 0.50\n", l)
		}
		return b.String(), lines
	}
	cases := map[string]func(t *testing.T, buffer string) string{
		"header removed": func(t *testing.T, buffer string) string { return strings.SplitN(buffer, "\n", 2)[1] },
		"stale buffer": func(t *testing.T, buffer string) string {
			return buffer // the base moves on below, before the owner saves
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			db, cfg := newHomeCfg(t)
			seedStated(t, db, cfg, "General", "Prefers alpha.", 0.7)
			mustSync(t, db, cfg)
			buffer := edit(t, readFile(t, cfg.TastePath()))
			if name == "stale buffer" {
				seedStated(t, db, cfg, "General", "Prefers a row added after the buffer loaded.", 0.7)
				mustSync(t, db, cfg)
			}
			add, lines := eleven()
			writeTaste(t, cfg, buffer+add)
			if err := ReadBack(db, cfg, cfg.TastePath()); err != nil {
				t.Fatal(err)
			}
			if !HoldPending(cfg) || !HoldAcceptable(cfg) {
				t.Fatalf("setup: pending=%v acceptable=%v", HoldPending(cfg), HoldAcceptable(cfg))
			}

			var err error
			out := captureStderr(t, func() { err = AcceptHeldEdits(db, cfg, false) })
			if err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("without --yes the accept must refuse and name --yes, got %v", err)
			}
			for _, l := range lines {
				if !strings.Contains(out, l) {
					t.Fatalf("the full list must be printed before the refusal, missing %q in %q", l, out)
				}
			}
			if !HoldPending(cfg) || countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement LIKE 'Prefers the over-cap line%' AND valid_until IS NULL`) != 0 {
				t.Fatal("a refused accept must apply nothing and keep the hold")
			}

			out = captureStderr(t, func() { err = AcceptHeldEdits(db, cfg, true) })
			if err != nil {
				t.Fatalf("--yes must override the line cap: %v", err)
			}
			for _, l := range lines {
				if !strings.Contains(out, l) {
					t.Fatalf("the full list must be printed before applying, missing %q in %q", l, out)
				}
				if n := countQuery(t, db, `SELECT count(*) FROM preferences WHERE statement=? AND valid_until IS NULL`, l); n != 1 {
					t.Fatalf("live rows for %q = %d, want 1", l, n)
				}
			}
			if HoldPending(cfg) {
				t.Fatal("an accepted hold must be cleared")
			}
		})
	}
}

// A discard that cannot archive the held copy must stop: the hold, the held
// copy and TASTE.md stay exactly as they were.
func TestDiscardAbortsWhenArchiveFails(t *testing.T) {
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
	taste := readFile(t, cfg.TastePath())
	// Park the held copy in a read-only directory: renaming it out fails.
	dir := filepath.Join(cfg.Home, "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "held.md")
	if err := os.Rename(h.HeldPath, moved); err != nil {
		t.Fatal(err)
	}
	h.HeldPath = moved
	b, _ := json.Marshal(h)
	if err := os.WriteFile(holdFilePath(cfg), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = DiscardHold(cfg)
	if err == nil || !strings.Contains(err.Error(), "palate taste") {
		t.Fatalf("a discard that cannot archive the held copy must fail with a clear message, got %v", err)
	}
	if !HoldPending(cfg) {
		t.Fatal("the hold must stay in place")
	}
	if _, serr := os.Stat(h.HeldPath); serr != nil {
		t.Fatalf("the held copy must still exist: %v", serr)
	}
	if readFile(t, cfg.TastePath()) != taste {
		t.Fatal("TASTE.md must be unchanged")
	}
}
