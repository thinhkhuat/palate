package render

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
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
	q := `SELECT category, statement, confidence, source, trust_tier FROM preferences WHERE valid_until IS NULL`
	if !opts.IncludeConflicted {
		q += ` AND id NOT IN (SELECT conflicting_id FROM conflicts WHERE resolved_at IS NULL)`
	}
	q += ` ORDER BY category ASC, confidence DESC, statement ASC`
	rows, err := conn.Query(q)
	if err != nil {
		return "", err
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
	}
	sort.Strings(cats)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<!-- auto-maintained by palate after event #%d; edit with `palate prefer` / `palate forget`, not by hand -->\n", lastEvent))
	b.WriteString("# Taste\n\n")
	if len(cats) == 0 {
		b.WriteString("_No preferences learned yet._\n")
		return b.String(), nil
	}
	for _, cat := range cats {
		b.WriteString(fmt.Sprintf("## %s\n\n", cat))
		for _, p := range byCat[cat] {
			b.WriteString(fmt.Sprintf("- %s. Confidence: %.2f (%s, %s)\n", strings.TrimSuffix(p.stmt, "."), p.conf, p.source, p.tier))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
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

func Reimport(conn *sql.DB, path string) (int, error) {
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
			sum := sha256.Sum256([]byte(currentCat + "\x00" + stmt))
			id := fmt.Sprintf("import-%x", sum[:8])
			_, err = conn.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, trust_tier, veracity) VALUES(?,?,?,?,?,?,?,?,?)`,
				id, currentCat, stmt, 0.4, 0.5, "imported", `["imported"]`, "IMPORTED", "imported")
			if err == nil {
				imported++
				// Through the same funnel as every other mutation, so the
				// auto-maintained TASTE.md picks the import up immediately.
				_ = storage.AppendEvent(conn, "import", fmt.Sprintf(`{"id":%q,"c":0.4,"signal":"imported"}`, id))
			}
		}
	}
	return imported, nil
}
