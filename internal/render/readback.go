package render

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

// maxHandEditAdditions is the line-count above which one read-back is held
// for the owner instead of applied (req 5, red-team owner default): a large
// batch of new lines is exactly what a prompt-injected or "tidying" edit
// would look like, so it waits for `palate taste --accept-edits`.
const maxHandEditAdditions = 10

// HoldLineCap is maxHandEditAdditions for callers that word the limit (the
// notice).
const HoldLineCap = maxHandEditAdditions

// massDeletionRatio is the fraction of the base's bullets a hand edit may
// delete before it is treated as a mass deletion (deletions held,
// additions still applied).
const massDeletionRatio = 0.5

// ReadBack promotes the owner's hand edits to TASTE.md into the ledger:
// added or reworded lines become STATED preferences tagged taste_edit,
// deleted lines soft-retire. Gated only by profile.read_back_edits — unlike
// Sync's own internal call, an explicit ReadBack (a hook, or `palate taste`)
// is the owner asking, so render.auto does not gate it. Runs inside
// withRenderLock, so it can never interleave with Sync's own render.
func ReadBack(conn *sql.DB, cfg config.Config, path string) error {
	if !cfg.Profile.ReadBackEdits {
		return nil
	}
	return withRenderLock(cfg, func() error {
		return readBack(conn, cfg, path)
	})
}

// readBack is the unlocked body ReadBack and Sync's own internal call both
// run. Sync already holds state/render.lock, so it calls this directly
// instead of taking the lock a second time.
func readBack(conn *sql.DB, cfg config.Config, path string) error {
	// A hold is already pending: the disk file is exactly what triggered it,
	// untouched since (Sync refuses to overwrite TASTE.md while held), so
	// re-running the detection below would just recompute — and re-apply —
	// the same diff. Wait for `palate taste` or `--accept-edits` instead.
	if holdPending(cfg) {
		return nil
	}

	disk, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	diskStr := string(disk)

	basePath := cfg.StatePath("TASTE.base.md")
	baseBytes, baseErr := os.ReadFile(basePath)
	hasBase := baseErr == nil
	baseStr := string(baseBytes)

	sha, hasSha := parseHeaderSha(diskStr)
	switch {
	case hasSha && bodySha(extractBody(diskStr)) == sha:
		return nil // clean: the header already matches its own body
	case hasSha && !hasBase:
		return writeHold(cfg, diskStr, "the header carries a sha but no base is on record", guardUnknownBase, "")
	case hasSha && bodySha(baseStr) != sha:
		return writeHold(cfg, diskStr, "the base on record does not match the header's sha", guardMovedBase, baseStr)
	case !hasSha && !hasBase:
		return nil // first run: no sha minted yet, nothing to compare against
	case !hasSha && extractBody(diskStr) == baseStr:
		return nil // an old-format header on otherwise-unchanged content
	case !hasSha && !hasAutoHeader(diskStr):
		// The owner took the header comment out while editing: the file is his
		// edit of an unknown revision, not an older palate's dump.
		return writeHold(cfg, diskStr, "the header line was removed, so the file cannot be matched to its base", guardNoHeader, baseStr)
	case !hasSha:
		return writeHold(cfg, diskStr, "an older-format edit with a known base cannot be verified safely", guardUnknownBase, "")
	}

	if modelEditedPath(cfg, path) {
		return writeHold(cfg, diskStr, "the assistant edited this file", guardAssistant, baseStr)
	}

	additions, deletions := computeBulletDiff(extractBody(diskStr), baseStr)
	if len(additions) > maxHandEditAdditions {
		return writeHold(cfg, diskStr, fmt.Sprintf("%d added lines exceeds the %d-line hold", len(additions), maxHandEditAdditions), guardAdditions, baseStr)
	}

	if len(additions)+len(deletions) > 0 {
		// No backup, no write: the edit is stashed instead of applied, so it
		// is neither lost nor applied unprotected.
		if _, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "taste-edit"); err != nil {
			return writeHold(cfg, diskStr, "no backup could be taken before applying it: "+err.Error(), guardBackup, baseStr)
		}
	}
	applyAdditions(conn, cfg, additions)

	if isMassDeletion(diskStr, baseStr, deletions) {
		return writeHold(cfg, diskStr, "more than half the profile would be deleted", guardDeletions, baseStr)
	}
	applyDeletions(conn, deletions)
	return nil
}

// hasAutoHeader reports whether content still has palate's own header comment
// (any format) above the "# Taste" line.
func hasAutoHeader(content string) bool {
	head := content
	if idx := strings.Index(content, tasteMarker); idx >= 0 {
		head = content[:idx]
	}
	return strings.Contains(head, "auto-maintained by palate")
}

// isMassDeletion reports whether deletions is more than massDeletionRatio of
// the base's bullets, or any deletion at all from a file with no "# Taste"
// marker (a mangled edit): those deletions wait for the owner.
func isMassDeletion(diskStr, baseStr string, deletions []bulletEntry) bool {
	if len(deletions) == 0 {
		return false
	}
	if !strings.Contains(diskStr, "# Taste") {
		return true
	}
	baseCount := len(bulletEntries(baseStr))
	return baseCount > 0 && float64(len(deletions))/float64(baseCount) > massDeletionRatio
}

// bulletEntry is one rendered bullet's statement plus the "## Category"
// heading nearest above it in its own file — the heading a hand-added line
// gets filed under.
type bulletEntry struct {
	stmt    string
	heading string
}

// bulletEntries walks a rendered body (no header) in line order, tracking
// the current heading, and returns one entry per bullet. Blank statements
// (a stray "- ") never happen from BulletLine's own output but are skipped
// defensively for a hand-typed body.
func bulletEntries(body string) []bulletEntry {
	var out []bulletEntry
	heading := "General"
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			heading = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
		case strings.HasPrefix(trimmed, "- "):
			if stmt := statementOf(trimmed); stmt != "" {
				out = append(out, bulletEntry{stmt: stmt, heading: heading})
			}
		}
	}
	return out
}

// computeBulletDiff is a pure statement-text diff between the disk body and
// the base body: disk − base is additions (in disk's own line order), base −
// disk is deletions (in base's own line order). A line whose text is
// unchanged between the two — even if the row backing it retired in the
// meantime — is neither: it is not "new", it is simply still there.
func computeBulletDiff(diskBody, baseBody string) (additions, deletions []bulletEntry) {
	diskEntries, baseEntries := bulletEntries(diskBody), bulletEntries(baseBody)
	diskSet, baseSet := map[string]bool{}, map[string]bool{}
	for _, e := range diskEntries {
		diskSet[e.stmt] = true
	}
	for _, e := range baseEntries {
		baseSet[e.stmt] = true
	}
	for _, e := range diskEntries {
		if !baseSet[e.stmt] {
			additions = append(additions, e)
		}
	}
	for _, e := range baseEntries {
		if !diskSet[e.stmt] {
			deletions = append(deletions, e)
		}
	}
	return additions, deletions
}

// applyAdditions stores each addition as the owner's own stated taste
// (SignalExplicitPrefer, judged confidence 0.95, tag taste_edit — phase-01
// req 9), reinforcing an existing live row of the exact same statement
// instead of inserting a twin. A line the safety scanner flags is refused,
// counted, and logged as its own event rather than silently dropped.
func applyAdditions(conn *sql.DB, cfg config.Config, additions []bulletEntry) {
	for _, a := range additions {
		if scan := safety.Scan(a.stmt); scan.Flagged {
			storage.BumpDrop(conn, "taste_edit")
			_ = storage.AppendEvent(conn, "taste_edit_refused", fmt.Sprintf(`{"reason":%q}`, scan.Reason+" pattern"))
			continue
		}
		applyHandEditAddition(conn, cfg, a)
	}
}

func applyHandEditAddition(conn *sql.DB, cfg config.Config, a bulletEntry) {
	existingID, found := liveIDByStatement(conn, a.stmt)
	sig := learn.Signal{Type: learn.SignalExplicitPrefer}
	assessment := learn.Assessment{Statement: a.stmt, Category: a.heading, Confidence: 0.95, By: "taste_edit"}
	if found {
		assessment.Relation = learn.RelationSame
		assessment.TargetID = existingID
	} else {
		assessment.Relation = learn.RelationNew
	}
	p, aerr := learn.ApplyJudged(conn, cfg, sig, assessment)
	if aerr != nil || p == nil {
		fmt.Fprintf(os.Stderr, "palate: read-back could not store %q: %v\n", a.stmt, aerr)
		return
	}
	tagTasteEdit(conn, p.ID)
}

func tagTasteEdit(conn *sql.DB, id string) {
	var prov string
	if err := conn.QueryRow(`SELECT provenance FROM preferences WHERE id=?`, id).Scan(&prov); err != nil {
		return
	}
	if strings.Contains(prov, `"`+learn.TagTasteEdit+`"`) {
		return
	}
	_, _ = conn.Exec(`UPDATE preferences SET provenance=json_insert(provenance, '$[#]', ?) WHERE id=?`, learn.TagTasteEdit, id)
}

// applyDeletions soft-retires the live row matching each deleted bullet's
// statement text (normStatement) (a no-op when none matches — already gone, or never
// there, which computeBulletDiff already made idempotent for a repeated
// call before the next Sync advances the base).
func applyDeletions(conn *sql.DB, deletions []bulletEntry) {
	for _, d := range deletions {
		retireByStatement(conn, d.stmt)
	}
}

// liveIDByStatement finds the first live row whose statement equals stmt under
// normStatement. TASTE.md drops a statement's trailing period, so an exact SQL
// match against the stored text never pairs a rendered line with its row.
func liveIDByStatement(conn *sql.DB, stmt string) (string, bool) {
	rows, err := conn.Query(`SELECT id, statement FROM preferences WHERE valid_until IS NULL ORDER BY created_at, id`)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	want := normStatement(stmt)
	for rows.Next() {
		var id, stored string
		if rows.Scan(&id, &stored) == nil && normStatement(stored) == want {
			return id, true
		}
	}
	return "", false
}

func retireByStatement(conn *sql.DB, stmt string) bool {
	id, found := liveIDByStatement(conn, stmt)
	if !found {
		return false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=?`, now, id); err != nil {
		return false
	}
	_ = storage.AppendEvent(conn, "taste_edit_forget", fmt.Sprintf(`{"id":%q,"reason":%q}`, id, "deleted from TASTE.md by hand"))
	return true
}

// modelEditedPath reports whether path's PostToolUse snapshot is on record
// in state/model_edits.json — the assistant, not the owner, wrote the bytes
// currently on disk (red-team H4, owner default Q1).
func modelEditedPath(cfg config.Config, path string) bool {
	b, err := os.ReadFile(cfg.StatePath("model_edits.json"))
	if err != nil {
		return false
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(b, &state) != nil {
		return false
	}
	_, ok := state[path]
	return ok
}

// Why a hand edit was held. Accept enforces every guard except the one that
// held it (that one is the owner's to override); guardUnknownBase holds are
// discard-only.
const (
	guardAdditions   = "additions"    // more than maxHandEditAdditions new lines
	guardDeletions   = "deletions"    // more than massDeletionRatio of the base deleted
	guardAssistant   = "assistant"    // model_edits.json says the assistant wrote the bytes
	guardBackup      = "backup"       // the pre-apply backup failed
	guardMovedBase   = "moved-base"   // saved from a body the recorded base no longer is
	guardNoHeader    = "no-header"    // saved with palate's header line removed: the base cannot be verified, treated like moved-base
	guardUnknownBase = "unknown-base" // no base, no sha or an older format: nothing to compare against
)

// baseNotVerified reports whether the held file cannot be matched to its base
// (the editor-buffer case or a removed header): accept applies its added lines
// only and lists any line that would bring back text that left the profile.
func baseNotVerified(guard string) bool {
	return guard == guardMovedBase || guard == guardNoHeader
}

// readbackHold is state/readback.hold.json's shape: where the disk bytes
// that could not be applied safely were stashed, why, when, and the base
// profile body the diff must be taken against on accept (never today's base,
// which Sync keeps rewriting while the hold is pending). A hold written by an
// older binary carries no guard or base and is discard-only.
type readbackHold struct {
	HeldPath string `json:"held_path"`
	BasePath string `json:"base_path,omitempty"`
	Guard    string `json:"guard,omitempty"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

func holdFilePath(cfg config.Config) string { return cfg.StatePath("readback.hold.json") }

// HoldPending reports whether a hand edit is stashed awaiting the owner's
// `palate taste --accept-edits` or `palate taste` (discard).
func HoldPending(cfg config.Config) bool {
	return holdPending(cfg)
}

func holdPending(cfg config.Config) bool {
	_, err := os.Stat(holdFilePath(cfg))
	return err == nil
}

func holdAcceptable(h readbackHold) bool {
	return h.BasePath != "" && h.Guard != "" && h.Guard != guardUnknownBase
}

// HoldAcceptable reports whether the pending hold can be applied with
// `palate taste --accept-edits`. False for a hold with no verifiable base (an
// old-format file, a missing base, a hold from an older binary) or an
// unreadable one: those can only be discarded.
func HoldAcceptable(cfg config.Config) bool {
	h, err := readHold(cfg)
	return err == nil && holdAcceptable(h)
}

// writeHold stashes diskStr (the bytes Sync must not overwrite) under
// state/TASTE.held-<ts>.md and records why in state/readback.hold.json, with
// baseStr (the base body the held file was compared against; "" when there is
// none to trust) beside it. A hold already pending keeps its own first
// snapshot — the same "first wins" rule storage.BackupBefore uses — rather
// than being replaced by whatever a later, still-blocked Sync recomputes.
func writeHold(cfg config.Config, diskStr, reason, guard, baseStr string) error {
	if holdPending(cfg) {
		return nil
	}
	if err := os.MkdirAll(cfg.StateDir(), 0700); err != nil {
		return err
	}
	ts := time.Now().UTC().Format("20060102-150405.000000000")
	h := readbackHold{HeldPath: cfg.StatePath(fmt.Sprintf("TASTE.held-%s.md", ts)), Guard: guard, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)}
	if guard != guardUnknownBase {
		h.BasePath = cfg.StatePath(fmt.Sprintf("TASTE.held-base-%s.md", ts))
		if err := os.WriteFile(h.BasePath, []byte(baseStr), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(h.HeldPath, []byte(diskStr), 0600); err != nil {
		return err
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(holdFilePath(cfg), b, 0600)
}

func readHold(cfg config.Config) (readbackHold, error) {
	var h readbackHold
	b, err := os.ReadFile(holdFilePath(cfg))
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("corrupt %s: %w", holdFilePath(cfg), err)
	}
	return h, nil
}

func clearHold(cfg config.Config, h readbackHold) {
	if h.HeldPath != "" {
		_ = os.Remove(h.HeldPath)
	}
	if h.BasePath != "" {
		_ = os.Remove(h.BasePath)
	}
	_ = os.Remove(holdFilePath(cfg))
}

// AcceptHeldEdits applies a pending hold's stashed edit against the base body
// recorded when the hold was made — never today's base, which has moved on —
// as the owner's explicit override of the guard that held it (the line cap,
// the mass-deletion guard); every other guard still applies. Bringing back a
// line that has left the profile, and adding more lines than the cap, are the
// owner's to override with confirm (--yes), after every such line has been
// listed, whatever guard held the edit. A hold whose file was saved from a body the base no longer matches (or with its
// header line removed) applies its added lines only, never deletions. A hold
// with no verifiable base (an old-format file, a missing base, a hold from an
// older binary) is refused: it can only be discarded. It then clears the stale
// on-disk TASTE.md so the next Sync starts clean instead of re-detecting the
// very edit it just accepted; a TASTE.md saved again after the hold is archived
// first (archiveLaterEdits). Errors if no hold is pending.
func AcceptHeldEdits(conn *sql.DB, cfg config.Config, confirm bool) error {
	return withRenderLock(cfg, func() error {
		h, err := readHold(cfg)
		if err != nil {
			return fmt.Errorf("no pending hand-edit hold to accept: %w", err)
		}
		if !holdAcceptable(h) {
			return fmt.Errorf("this held edit (%s) cannot be applied safely; run `palate taste` to discard it", h.Reason)
		}
		heldBytes, err := os.ReadFile(h.HeldPath)
		if err != nil {
			return fmt.Errorf("held copy %s is missing: %w", h.HeldPath, err)
		}
		baseBytes, err := os.ReadFile(h.BasePath)
		if err != nil {
			return fmt.Errorf("the base recorded with this hold is missing (%v); run `palate taste` to discard the held edit", err)
		}
		heldStr, baseStr := string(heldBytes), string(baseBytes)
		additions, deletions := computeBulletDiff(extractBody(heldStr), baseStr)
		if baseNotVerified(h.Guard) {
			deletions = nil
		}
		// Two things need the owner's explicit --yes, each listed in full first:
		// lines that would come back after leaving the profile (whatever guard
		// held the edit), and more added lines than the cap (the cap-held hold
		// included).
		restored := restoredLines(conn, additions)
		overCap := len(additions) > maxHandEditAdditions
		if len(restored) > 0 {
			printLines(fmt.Sprintf("this edit would bring back %d line(s) that have since left the profile:", len(restored)), restored)
		}
		if overCap {
			stmts := make([]string, len(additions))
			for i, a := range additions {
				stmts[i] = a.stmt
			}
			printLines(fmt.Sprintf("this edit adds %d lines, over the %d-line limit:", len(additions), maxHandEditAdditions), stmts)
		}
		if (len(restored) > 0 || overCap) && !confirm {
			var why []string
			if len(restored) > 0 {
				why = append(why, fmt.Sprintf("would bring back %d line(s) that have since left the profile (%s)", len(restored), quoteLines(restored)))
			}
			if overCap {
				why = append(why, fmt.Sprintf("adds %d lines, over the %d-line limit", len(additions), maxHandEditAdditions))
			}
			subject := "the held edit"
			switch h.Guard {
			case guardMovedBase:
				subject = "this edit was saved from an out-of-date copy of TASTE.md and"
			case guardNoHeader:
				subject = "this edit was saved with its header line removed and"
			}
			return fmt.Errorf("%s %s; run `palate taste --accept-edits --yes` to apply it anyway, or `palate taste` to discard it", subject, strings.Join(why, " and "))
		}
		if h.Guard != guardDeletions && isMassDeletion(heldStr, baseStr, deletions) {
			return fmt.Errorf("the held edit deletes %d of %d lines, over the mass-deletion limit; run `palate taste` to discard it", len(deletions), len(bulletEntries(baseStr)))
		}
		if len(additions)+len(deletions) > 0 {
			if _, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "taste-edit-accept"); err != nil {
				return fmt.Errorf("backup before accept-edits: %w", err)
			}
		}
		if err := archiveLaterEdits(cfg, heldStr, true); err != nil {
			return err
		}
		note := ""
		if baseNotVerified(h.Guard) {
			note = " (deletions skipped: the file cannot be matched to the profile it was written from)"
		}
		fmt.Fprintf(os.Stderr, "palate: accept-edits: applying %d added and %d deleted line(s)%s\n", len(additions), len(deletions), note)
		applyAdditions(conn, cfg, additions)
		applyDeletions(conn, deletions)
		clearHold(cfg, h)
		forgetModelEdit(cfg)
		_ = os.Remove(cfg.TastePath())
		return nil
	})
}

// restoredLines are the additions that would bring a line back to the ledger
// that has left it: its text belongs to a closed row and to no live row, or is
// the old wording of a row that was reworded since: by the dream pass (a
// rewrite, or a merge that overwrote its survivor) or by the learner (a refine).
func restoredLines(conn *sql.DB, additions []bulletEntry) []string {
	closed, live := map[string]bool{}, map[string]bool{}
	if rows, err := conn.Query(`SELECT statement, valid_until IS NULL FROM preferences`); err == nil {
		for rows.Next() {
			var stmt string
			var isLive bool
			if rows.Scan(&stmt, &isLive) == nil {
				if isLive {
					live[normStatement(stmt)] = true
				} else {
					closed[normStatement(stmt)] = true
				}
			}
		}
		rows.Close()
	}
	if rows, err := conn.Query(`SELECT json_extract(payload,'$.from') FROM events WHERE event_type IN ('consolidate_rewrite','consolidate_merge','refine')`); err == nil {
		for rows.Next() {
			var from sql.NullString
			if rows.Scan(&from) == nil && from.Valid {
				closed[normStatement(from.String)] = true
			}
		}
		rows.Close()
	}
	var out []string
	for _, a := range additions {
		if closed[a.stmt] && !live[a.stmt] {
			out = append(out, a.stmt)
		}
	}
	return out
}

// printLines writes heading and then every line, one per row, to stderr: the
// owner sees the whole list before he confirms.
func printLines(heading string, lines []string) {
	fmt.Fprintf(os.Stderr, "palate: accept-edits: %s\n", heading)
	for _, l := range lines {
		fmt.Fprintf(os.Stderr, "  - %s\n", l)
	}
}

// archiveLaterEdits keeps TASTE.md when it is not the held copy: a hold freezes
// Sync, so every edit the owner saved after the hold lives only there, and
// accept and discard both rewrite the file. A TASTE.md that differs from the
// held copy (or whose held copy is unknown) is copied to
// state/TASTE.after-hold-<ts>.md and the path is printed; the caller stops if
// the copy cannot be written.
func archiveLaterEdits(cfg config.Config, heldStr string, heldKnown bool) error {
	cur, err := os.ReadFile(cfg.TastePath())
	if err != nil || (heldKnown && string(cur) == heldStr) {
		return nil
	}
	dst := cfg.StatePath("TASTE.after-hold-" + time.Now().UTC().Format("20060102-150405.000000000") + ".md")
	if err := os.WriteFile(dst, cur, 0600); err != nil {
		return fmt.Errorf("TASTE.md was saved again after the hold and could not be archived (%v); nothing was changed", err)
	}
	fmt.Fprintf(os.Stderr, "palate: TASTE.md was saved again after the hold was made; its newer contents are kept at %s\n", dst)
	return nil
}

// quoteLines joins up to ten lines as quoted text, counting the rest.
func quoteLines(lines []string) string {
	const shown = 10
	var parts []string
	for i, l := range lines {
		if i == shown {
			parts = append(parts, fmt.Sprintf("and %d more", len(lines)-shown))
			break
		}
		parts = append(parts, fmt.Sprintf("%q", l))
	}
	return strings.Join(parts, "; ")
}

// HoldNeedsConfirm reports whether accepting the pending hold may need
// `--yes`: it was saved from a copy of TASTE.md that cannot be matched to the
// profile (an out-of-date buffer, or a removed header line), so lines that have
// since left it could come back, and it may add more lines than the cap.
func HoldNeedsConfirm(cfg config.Config) bool {
	h, err := readHold(cfg)
	return err == nil && holdAcceptable(h) && baseNotVerified(h.Guard)
}

// HoldHeaderRemoved reports whether the pending hold is an edit whose palate
// header line was removed, so the notice does not call it an out-of-date copy.
func HoldHeaderRemoved(cfg config.Config) bool {
	h, err := readHold(cfg)
	return err == nil && h.Guard == guardNoHeader
}

// forgetModelEdit drops TASTE.md's entry from state/model_edits.json: the
// owner has answered the question "did the assistant write this file", so the
// entry must not make his next, real edit look like the assistant's.
func forgetModelEdit(cfg config.Config) {
	path := cfg.StatePath("model_edits.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(b, &state) != nil {
		return
	}
	if _, ok := state[cfg.TastePath()]; !ok {
		return
	}
	delete(state, cfg.TastePath())
	out, err := json.Marshal(state)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, out, 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// archiveFailed is the error of a discard that could not keep the held copy.
func archiveFailed(err error) error {
	return fmt.Errorf("could not archive the held edit (%v); nothing was changed — fix that and run `palate taste` again", err)
}

// DiscardHold throws away a pending hold without applying it, and removes the
// stale on-disk TASTE.md so the next Sync re-renders clean. The held copy is
// the only copy of the owner's edit, so it is archived under state/ as
// TASTE.discarded-<ts>.md, never deleted. A no-op (not an error) when no hold
// is pending. If the held copy cannot be archived, the discard stops with an
// error and changes nothing.
func DiscardHold(cfg config.Config) error {
	return withRenderLock(cfg, func() error {
		h, err := readHold(cfg)
		if err != nil && !holdPending(cfg) {
			return nil
		}
		heldStr, heldKnown := "", false
		if err == nil && h.HeldPath != "" {
			if b, rerr := os.ReadFile(h.HeldPath); rerr == nil {
				heldStr, heldKnown = string(b), true
			}
		}
		if aerr := archiveLaterEdits(cfg, heldStr, heldKnown); aerr != nil {
			return aerr
		}
		ts := time.Now().UTC().Format("20060102-150405.000000000")
		if err != nil {
			// An unreadable hold file leaves h empty and its held copy
			// unknown: archive the hold file itself, then clear it.
			if rerr := os.Rename(holdFilePath(cfg), cfg.StatePath("readback.hold.discarded-"+ts+".json")); rerr != nil && !os.IsNotExist(rerr) {
				return archiveFailed(rerr)
			}
		} else if h.HeldPath != "" {
			// A held copy that is already gone leaves nothing to keep; any other
			// failure stops the discard before it changes anything.
			if rerr := os.Rename(h.HeldPath, cfg.StatePath("TASTE.discarded-"+ts+".md")); rerr != nil && !os.IsNotExist(rerr) {
				return archiveFailed(rerr)
			}
		}
		clearHold(cfg, h)
		forgetModelEdit(cfg)
		_ = os.Remove(cfg.TastePath())
		return nil
	})
}
