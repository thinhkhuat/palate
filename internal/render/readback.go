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
		return writeHold(cfg, diskStr, "the header carries a sha but no base is on record")
	case hasSha && bodySha(baseStr) != sha:
		return writeHold(cfg, diskStr, "the base on record does not match the header's sha")
	case !hasSha && !hasBase:
		return nil // first run: no sha minted yet, nothing to compare against
	case !hasSha && extractBody(diskStr) == baseStr:
		return nil // an old-format header on otherwise-unchanged content
	case !hasSha:
		return writeHold(cfg, diskStr, "an older-format edit with a known base cannot be verified safely")
	}

	if modelEditedPath(cfg, path) {
		return writeHold(cfg, diskStr, "the assistant edited this file")
	}

	additions, deletions := computeBulletDiff(extractBody(diskStr), baseStr)
	if len(additions) > maxHandEditAdditions {
		return writeHold(cfg, diskStr, fmt.Sprintf("%d added lines exceeds the %d-line hold", len(additions), maxHandEditAdditions))
	}

	if len(additions)+len(deletions) > 0 {
		if _, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "taste-edit"); err != nil {
			fmt.Fprintf(os.Stderr, "palate: backup before taste-edit read-back: %v\n", err)
		}
	}
	applyAdditions(conn, cfg, additions)

	massDeletion := !strings.Contains(diskStr, "# Taste")
	if !massDeletion && len(deletions) > 0 {
		baseCount := len(bulletEntries(baseStr))
		if baseCount > 0 && float64(len(deletions))/float64(baseCount) > massDeletionRatio {
			massDeletion = true
		}
	}
	if massDeletion {
		if len(deletions) > 0 {
			return writeHold(cfg, diskStr, "more than half the profile would be deleted")
		}
		return nil
	}
	applyDeletions(conn, deletions)
	return nil
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
	var existingID string
	err := conn.QueryRow(`SELECT id FROM preferences WHERE statement=? AND valid_until IS NULL LIMIT 1`, a.stmt).Scan(&existingID)
	sig := learn.Signal{Type: learn.SignalExplicitPrefer}
	assessment := learn.Assessment{Statement: a.stmt, Category: a.heading, Confidence: 0.95, By: "taste_edit"}
	if err == nil && existingID != "" {
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
// exact statement text (a no-op when none matches — already gone, or never
// there, which computeBulletDiff already made idempotent for a repeated
// call before the next Sync advances the base).
func applyDeletions(conn *sql.DB, deletions []bulletEntry) {
	for _, d := range deletions {
		retireByStatement(conn, d.stmt)
	}
}

func retireByStatement(conn *sql.DB, stmt string) bool {
	var id string
	if err := conn.QueryRow(`SELECT id FROM preferences WHERE statement=? AND valid_until IS NULL LIMIT 1`, stmt).Scan(&id); err != nil {
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

// readbackHold is state/readback.hold.json's shape: where the disk bytes
// that could not be applied safely were stashed, why, and when.
type readbackHold struct {
	HeldPath string `json:"held_path"`
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

// writeHold stashes diskStr (the bytes Sync must not overwrite) under
// state/TASTE.held-<ts>.md and records why in state/readback.hold.json. A
// hold already pending keeps its own first snapshot — the same "first wins"
// rule storage.BackupBefore uses — rather than being replaced by whatever a
// later, still-blocked Sync recomputes.
func writeHold(cfg config.Config, diskStr, reason string) error {
	if holdPending(cfg) {
		return nil
	}
	if err := os.MkdirAll(cfg.StateDir(), 0700); err != nil {
		return err
	}
	ts := time.Now().UTC().Format("20060102-150405.000000000")
	heldPath := cfg.StatePath(fmt.Sprintf("TASTE.held-%s.md", ts))
	if err := os.WriteFile(heldPath, []byte(diskStr), 0600); err != nil {
		return err
	}
	h := readbackHold{HeldPath: heldPath, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)}
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
	_ = os.Remove(holdFilePath(cfg))
}

// AcceptHeldEdits applies a pending hold's stashed additions and deletions
// unconditionally — the owner's explicit override of whatever guard held it
// (a mass deletion, an unknown base, an assistant edit, or more than
// maxHandEditAdditions new lines). It then clears the stale on-disk
// TASTE.md so the next Sync starts clean instead of re-detecting the very
// edit it just accepted. Errors if no hold is pending.
func AcceptHeldEdits(conn *sql.DB, cfg config.Config) error {
	return withRenderLock(cfg, func() error {
		h, err := readHold(cfg)
		if err != nil {
			return fmt.Errorf("no pending hand-edit hold to accept: %w", err)
		}
		heldBytes, err := os.ReadFile(h.HeldPath)
		if err != nil {
			return fmt.Errorf("held copy %s is missing: %w", h.HeldPath, err)
		}
		baseBytes, _ := os.ReadFile(cfg.StatePath("TASTE.base.md"))
		additions, deletions := computeBulletDiff(extractBody(string(heldBytes)), string(baseBytes))
		if len(additions)+len(deletions) > 0 {
			if _, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "taste-edit-accept"); err != nil {
				fmt.Fprintf(os.Stderr, "palate: backup before accept-edits: %v\n", err)
			}
		}
		applyAdditions(conn, cfg, additions)
		applyDeletions(conn, deletions)
		clearHold(cfg, h)
		_ = os.Remove(cfg.TastePath())
		return nil
	})
}

// DiscardHold throws away a pending hold's stashed copy without applying it,
// and removes the stale on-disk TASTE.md so the next Sync re-renders clean.
// A no-op (not an error) when no hold is pending.
func DiscardHold(cfg config.Config) error {
	return withRenderLock(cfg, func() error {
		h, err := readHold(cfg)
		if err != nil {
			return nil
		}
		clearHold(cfg, h)
		_ = os.Remove(cfg.TastePath())
		return nil
	})
}
