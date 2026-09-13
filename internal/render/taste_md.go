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
// churn); each bullet ends with (source, TRUST_TIER) so a reader can see why
// the line exists. Reimport and Lint ignore everything after "Confidence:".
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
// "- statement. Confidence: 0.xx (source, TIER)" bullet per preference. It is
// the same bytes TASTE.md carries and the same bytes the SessionStart prewarm
// injects — one artefact, two readers (Command Code parity, decisions row 17).
// n is the number of preferences rendered.
func Body(conn *sql.DB, opts RenderOptions) (body string, n int, err error) {
	q := `SELECT category, statement, confidence, source, trust_tier FROM preferences WHERE valid_until IS NULL`
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
		stmt, source, tier string
		conf               float64
	}
	byCat := map[string][]row{}
	var cats []string
	seen := map[string]bool{}
	for rows.Next() {
		var cat string
		var r row
		if err := rows.Scan(&cat, &r.stmt, &r.conf, &r.source, &r.tier); err != nil {
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
			b.WriteString(fmt.Sprintf("- %s. Confidence: %.2f (%s, %s)\n", strings.TrimSuffix(p.stmt, "."), p.conf, p.source, p.tier))
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
		}
	}
	return diags
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
