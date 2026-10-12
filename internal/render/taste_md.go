package render

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/judge"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// RegisterAuto keeps <home>/TASTE.md current: every mutation marks the store
// dirty (storage.AppendEvent) and storage.Close — every CLI verb, hook and the
// detached distiller — or storage.FlushIfDirty (after each MCP write tool)
// re-renders the file. One registration point for the CLI and the MCP server.
// Fail-open: a render error is reported on stderr and never fails a hook.
func RegisterAuto(cfg config.Config) {
	if !cfg.Render.Auto {
		storage.SetOnDirtyFlush(nil)
		return
	}
	storage.SetOnDirtyFlush(func(db *sql.DB) {
		if _, err := Sync(db, cfg, cfg.TastePath()); err != nil {
			fmt.Fprintf(os.Stderr, "palate: auto-render TASTE.md: %v\n", err)
		}
	})
}

// RenderOptions is kept for API stability across the render/prewarm call
// sites; it carries no fields. The old IncludeConflicted toggle is gone —
// SelectProfile always excludes the strictly-lower-confidence side of an
// open conflict (learn/profile.go conflictLosers), so there is nothing left
// for a caller to override.
type RenderOptions struct{}

// tasteMarker is the boundary between the header comment and the profile
// body in both TASTE.md and the value state/TASTE.base.md stores. Body
// content (learn.BulletLine output) never starts a line with "# ", so a
// hand-typed line can't be mistaken for it.
const tasteMarker = "# Taste\n\n"

// tasteHeaderTemplate is the HTML-comment header line. %d is the highest
// event id at render time (no timestamp churn: an unchanged DB renders
// byte-identical); %s is the first 12 hex characters of sha256(body) — the
// contract ReadBack uses to tell "untouched" from "hand-edited" bytes.
const tasteHeaderTemplate = "<!-- auto-maintained by palate after event #%d; sha256=%s; edit freely: added or reworded lines become your stated taste, deleted lines are forgotten (restorable with `palate restore`) -->\n" + tasteMarker

// shaDigits is the fixed width of the sha256 prefix TASTE.md's header
// carries — always exactly this many hex characters, never estimated.
const shaDigits = 12

// tasteHeader renders the header line for lastEvent and sha (already the
// first shaDigits hex characters of sha256(body)).
func tasteHeader(lastEvent int64, sha string) string {
	return fmt.Sprintf(tasteHeaderTemplate, lastEvent, sha)
}

// tasteHeaderOverheadEventIDDigits is the worst-case event-id width used only
// to size the header's overhead, never to render it (req 1: "a fixed
// worst-case width ... for the event id"). 12 digits comfortably outlives
// any store this tool will ever see.
const tasteHeaderOverheadEventIDDigits = 999999999999

// TasteHeaderOverhead is TASTE.md's own header cost in estimated tokens,
// computed at a fixed worst-case width for the event id and the sha so the
// profile budget never depends on the selection's own output (req 1, L3).
func TasteHeaderOverhead(cfg config.Config) int {
	return learn.EstimateTokens(tasteHeader(tasteHeaderOverheadEventIDDigits, strings.Repeat("f", shaDigits)))
}

func bodySha(body string) string {
	sum := sha256.Sum256([]byte(body))
	return fmt.Sprintf("%x", sum)[:shaDigits]
}

// extractBody strips the header comment and the "# Taste" line from a
// rendered TASTE.md, leaving the category/bullet body — the same bytes
// state/TASTE.base.md stores and SessionPrewarm injects. A file with no
// "# Taste" marker at all (a badly mangled hand edit) returns its full
// content unchanged, so a bullet-level diff still has something to compare.
func extractBody(content string) string {
	if idx := strings.Index(content, tasteMarker); idx >= 0 {
		return content[idx+len(tasteMarker):]
	}
	return content
}

var headerShaRe = regexp.MustCompile(`sha256=([0-9a-f]{12})`)

// parseHeaderSha reads the sha256=<hex> field from content's header line,
// only looking before "# Taste" so a hand-typed bullet that happens to
// contain the string "sha256=" can never be mistaken for the header's own.
func parseHeaderSha(content string) (sha string, ok bool) {
	head := content
	if idx := strings.Index(content, tasteMarker); idx >= 0 {
		head = content[:idx]
	}
	m := headerShaRe.FindStringSubmatch(head)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Render produces the human-readable profile: the header (event id + sha)
// followed by Body's category/bullet text. n==0 still carries a header (a
// consistent sha over the empty body), so ReadBack's contract never needs a
// special case for an empty store.
func Render(conn *sql.DB, cfg config.Config, opts RenderOptions) (string, error) {
	var lastEvent int64
	_ = conn.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&lastEvent)
	body, n, err := Body(conn, cfg, opts)
	if err != nil {
		return "", err
	}
	if n == 0 {
		// The sha must cover exactly the text that follows the header — the
		// placeholder line included — so ReadBack's "does the header still
		// match its own body" check holds for an empty profile too.
		body = "_No preferences learned yet._\n"
	}
	return tasteHeader(lastEvent, bodySha(body)) + body, nil
}

// ProfileBudget is the token budget learn.SelectProfile fills against: the
// shared prewarm_session budget minus whichever header (SessionStart's or
// TASTE.md's) costs more, so the same budgeted profile serves both readers
// without either overflowing (req 1). Exported so every caller that needs
// the profile outside Body itself (recall's in_profile marker, `palate taste
// --cut`) uses the identical budget Body does, instead of a second,
// possibly-drifting copy of the formula.
func ProfileBudget(cfg config.Config) int {
	overhead := SessionOverhead(cfg)
	if h := TasteHeaderOverhead(cfg); h > overhead {
		overhead = h
	}
	budget := cfg.TokenBudgets.PrewarmSession - overhead
	if budget < 0 {
		budget = 0
	}
	return budget
}

// Body is the profile body: "## Category" sections (ascending), one
// "- statement. Confidence: 0.xx" bullet per preference (confidence
// descending within a category) — the same bytes TASTE.md's body carries and
// the same bytes the SessionStart prewarm injects (Command Code parity). Rows
// come from learn.SelectProfile: a deterministic, budgeted, hysteresis-aware
// subset of the live ledger, not a ranking cut applied at render time. n is
// the number of preferences rendered (len(profile.In)).
func Body(conn *sql.DB, cfg config.Config, opts RenderOptions) (body string, n int, err error) {
	profile, err := learn.SelectProfile(conn, cfg, ProfileBudget(cfg))
	if err != nil {
		return "", 0, err
	}
	return renderProfileBody(profile.In), len(profile.In), nil
}

// renderProfileBody groups rows by category ascending, confidence descending
// within each category (display order — independent of SelectProfile's own
// rank order, which governs the budget fill, not the reading order).
func renderProfileBody(rows []learn.Preference) string {
	byCat := map[string][]learn.Preference{}
	var cats []string
	seen := map[string]bool{}
	for _, p := range rows {
		if !seen[p.Category] {
			cats = append(cats, p.Category)
			seen[p.Category] = true
		}
		byCat[p.Category] = append(byCat[p.Category], p)
	}
	sort.Strings(cats)
	var b strings.Builder
	for _, cat := range cats {
		group := byCat[cat]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Confidence != group[j].Confidence {
				return group[i].Confidence > group[j].Confidence
			}
			return group[i].Statement < group[j].Statement
		})
		b.WriteString(fmt.Sprintf("## %s\n\n", cat))
		for _, p := range group {
			b.WriteString(learn.BulletLine(p) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func WriteFile(conn *sql.DB, cfg config.Config, path string) error {
	content, err := Render(conn, cfg, RenderOptions{})
	if err != nil {
		return err
	}
	if cur, rerr := os.ReadFile(path); rerr == nil && string(cur) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// Sync is the one writer of both TASTE.md and its profile membership events
// (profile_add/profile_cut). It runs inside withRenderLock, so it can never
// interleave with a hook-triggered ReadBack: inside the lock it (1) reads
// back the owner's hand edits when render.auto and profile.read_back_edits
// are both on, calling the unlocked readBack directly since the lock is
// already held; (2) selects the profile and diffs it against the previous
// membership, appending profile_add/profile_cut events; (3) computes the
// header from the post-event max event id and writes TASTE.md if it changed;
// (4) always writes state/TASTE.base.md, even while a read-back hold
// suppresses the TASTE.md write itself, so the base never falls out of step
// with what Sync last computed. wrote reports whether TASTE.md was written.
func Sync(conn *sql.DB, cfg config.Config, path string) (wrote bool, err error) {
	var out bool
	lockErr := withRenderLock(cfg, func() error {
		var serr error
		out, serr = sync(conn, cfg, path)
		return serr
	})
	if lockErr != nil {
		return false, lockErr
	}
	return out, nil
}

func sync(conn *sql.DB, cfg config.Config, path string) (bool, error) {
	if cfg.Render.Auto && cfg.Profile.ReadBackEdits {
		if err := readBack(conn, cfg, path); err != nil {
			fmt.Fprintf(os.Stderr, "palate: read-back %s: %v\n", path, err)
		}
	}
	if err := syncProfileEvents(conn, cfg); err != nil {
		return false, err
	}
	content, err := Render(conn, cfg, RenderOptions{})
	if err != nil {
		return false, err
	}
	wrote := false
	if !holdPending(cfg) {
		cur, rerr := os.ReadFile(path)
		if rerr != nil || string(cur) != content {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return false, err
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return false, err
			}
			wrote = true
		}
	}
	if err := writeBaseAtomic(cfg, extractBody(content)); err != nil {
		return false, err
	}
	return wrote, nil
}

// writeBaseAtomic writes state/TASTE.base.md — the exact body Sync just
// computed — through a temp file and rename, so a crash mid-write never
// leaves ReadBack comparing against a half-written base.
func writeBaseAtomic(cfg config.Config, body string) error {
	dir := cfg.StateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := cfg.StatePath("TASTE.base.md")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var confidenceRe = regexp.MustCompile(`Confidence:\s*0\.\d+|Confidence:\s*1\.00`)

// minStatementWords is the fewest words a bullet's statement can carry and
// still be a preference rather than the retired verbatim path's prefix with
// its body lost ("prefers:" alone, "code", …). "Use tabs" passes at two.
const minStatementWords = 2

func Lint(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("read error: %v", err)}
	}
	s := string(b)
	var diags []string
	if !strings.Contains(s, "# Taste") {
		diags = append(diags, "missing '# Taste' header")
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "Confidence:") {
			if !confidenceRe.MatchString(line) {
				diags = append(diags, fmt.Sprintf("line %d: bad confidence format: %q", i+1, line))
			}
			stmt := statementOf(line)
			// A statement that ends in a truncation ellipsis was cut by the
			// writer, so the file no longer carries what was learned — the
			// retired verbatim diff path's signature (2026-09-15).
			if strings.HasSuffix(stmt, "…") {
				diags = append(diags, fmt.Sprintf("line %d: truncated statement (ends with an ellipsis — the full text is lost): %q", i+1, stmt))
			}
			if n := len(strings.Fields(stmt)); n < minStatementWords {
				diags = append(diags, fmt.Sprintf("line %d: bullet is a fragment, not a preference sentence (%d word(s)): %q", i+1, n, stmt))
			}
		}
	}
	return diags
}

// normStatement is the one comparison form of a statement. The renderer drops
// a statement's trailing period (learn.BulletLine) and every statement stored
// by the distiller ends with one, so a rendered line only pairs with its row
// when both sides go through this: read-back of deletions and rewordings,
// hand-edit additions matching an existing row, the first-run membership
// seed, and Reimport.
func normStatement(s string) string {
	return strings.TrimSpace(strings.TrimRightFunc(s, func(r rune) bool { return r == '.' || unicode.IsSpace(r) }))
}

// statementOf strips the "- " bullet marker, the retired verbatim path's
// "prefers:" prefix, and everything from the trailing "Confidence:" field,
// leaving the statement itself in normStatement form.
func statementOf(bullet string) string {
	s := strings.TrimPrefix(bullet, "- ")
	if idx := strings.LastIndex(s, "Confidence:"); idx >= 0 {
		s = s[:idx]
	}
	s = normStatement(s)
	if len(s) >= 8 && strings.EqualFold(s[:8], "prefers:") {
		s = normStatement(s[8:])
	}
	return s
}

func Reimport(conn *sql.DB, cfg config.Config, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := string(b)
	var currentCat string
	imported := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			currentCat = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if strings.HasPrefix(line, "- ") {
			stmt := strings.TrimPrefix(line, "- ")
			if idx := strings.LastIndex(stmt, "Confidence:"); idx >= 0 {
				stmt = strings.TrimSpace(stmt[:idx])
			}
			stmt = normStatement(stmt)
			if stmt == "" {
				continue
			}
			if _, found := liveIDByStatement(conn, stmt); found {
				continue
			}
			// Every imported line is judged like any other write: with a
			// gateway it gets a real confidence and a same/refines/contradicts
			// verdict against the rows already there; without one it lands at
			// the imported weight (0.4). The tier stays IMPORTED either way.
			if _, _, err := judge.Apply(context.Background(), conn, cfg, judge.Input{Statement: stmt, Signal: learn.SignalImported, Category: currentCat}, ""); err == nil {
				imported++
			}
		}
	}
	return imported, nil
}
