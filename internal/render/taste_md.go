package render

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
		if _, err := Sync(db, cfg.TastePath(), RenderOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "palate: auto-render TASTE.md: %v\n", err)
		}
	})
}

type RenderOptions struct {
	IncludeConflicted bool
}

// Render produces the human-readable profile. The header carries the id of
// the last event so an unchanged DB renders byte-identical (no timestamp
// churn). Bullets are command-code shaped — "- statement. Confidence: 0.xx"
// with no internal metadata suffix: what the user reviews is exactly what the
// agent gets, and the agent never spends tokens on (source, TRUST_TIER).
// Provenance stays queryable via `palate recall -v` and the DB itself.
// Reimport and Lint ignore everything after "Confidence:".
func Render(conn *sql.DB, opts RenderOptions) (string, error) {
	var lastEvent int64
	_ = conn.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&lastEvent)
	body, n, err := Body(conn, opts)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<!-- auto-maintained by palate after event #%d; edit with `palate prefer` / `palate forget`, not by hand -->\n", lastEvent))
	b.WriteString("# Taste\n\n")
	if n == 0 {
		b.WriteString("_No preferences learned yet._\n")
		return b.String(), nil
	}
	b.WriteString(body)
	return b.String(), nil
}

// Body is the profile without the header: "## Category" sections, one
// "- statement. Confidence: 0.xx" bullet per preference. It is the same bytes
// TASTE.md carries and the same bytes the SessionStart prewarm injects — one
// artefact, two readers (Command Code parity, decisions row 17). Rows sort by
// category, then confidence descending: the strongest taste in each section
// reads first, and prewarm truncation (which drops trailing lines) drops the
// weakest rows first. n is the number of preferences rendered.
func Body(conn *sql.DB, opts RenderOptions) (body string, n int, err error) {
	// Rulebook-tagged rows (learn.RulebookOverlap in provenance) merely
	// restate the user's curated rulebook — suppressed here (and therefore
	// from the SessionStart prewarm, which renders the same bytes) until
	// consolidation retires them.
	q := `SELECT category, statement, confidence FROM preferences WHERE valid_until IS NULL
		AND provenance NOT LIKE '%"` + learn.RulebookOverlap + `"%'`
	if !opts.IncludeConflicted {
		q += ` AND id NOT IN (SELECT conflicting_id FROM conflicts WHERE resolved_at IS NULL)`
	}
	q += ` ORDER BY category ASC, confidence DESC, statement ASC`
	rows, err := conn.Query(q)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	type row struct {
		stmt string
		conf float64
	}
	byCat := map[string][]row{}
	var cats []string
	seen := map[string]bool{}
	for rows.Next() {
		var cat string
		var r row
		if err := rows.Scan(&cat, &r.stmt, &r.conf); err != nil {
			continue
		}
		if !seen[cat] {
			cats = append(cats, cat)
			seen[cat] = true
		}
		byCat[cat] = append(byCat[cat], r)
		n++
	}
	sort.Strings(cats)
	var b strings.Builder
	for _, cat := range cats {
		b.WriteString(fmt.Sprintf("## %s\n\n", cat))
		for _, p := range byCat[cat] {
			b.WriteString(fmt.Sprintf("- %s. Confidence: %.2f\n", strings.TrimSuffix(p.stmt, "."), p.conf))
		}
		b.WriteString("\n")
	}
	return b.String(), n, nil
}

func WriteFile(conn *sql.DB, path string, opts RenderOptions) error {
	_, err := Sync(conn, path, opts)
	return err
}

// Sync renders and writes path only when the bytes changed, so the file's
// mtime moves on real change alone. wrote reports whether it wrote.
func Sync(conn *sql.DB, path string, opts RenderOptions) (wrote bool, err error) {
	content, err := Render(conn, opts)
	if err != nil {
		return false, err
	}
	if cur, rerr := os.ReadFile(path); rerr == nil && string(cur) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(content), 0644)
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

// statementOf strips the "- " bullet marker, the retired verbatim path's
// "prefers:" prefix, and everything from the trailing "Confidence:" field,
// leaving the statement itself.
func statementOf(bullet string) string {
	s := strings.TrimPrefix(bullet, "- ")
	if idx := strings.LastIndex(s, "Confidence:"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimRight(strings.TrimSpace(s), ".")
	if len(s) >= 8 && strings.EqualFold(s[:8], "prefers:") {
		s = strings.TrimSpace(s[8:])
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
			stmt = strings.TrimSuffix(strings.TrimSpace(stmt), ".")
			if stmt == "" {
				continue
			}
			var exists string
			err := conn.QueryRow(`SELECT id FROM preferences WHERE statement=? AND valid_until IS NULL LIMIT 1`, stmt).Scan(&exists)
			if err == nil {
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
