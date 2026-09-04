package render

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

type RenderOptions struct {
	IncludeConflicted bool
}

func Render(conn *sql.DB, opts RenderOptions) (string, error) {
	q := `SELECT category, statement, confidence FROM preferences WHERE valid_until IS NULL`
	if !opts.IncludeConflicted {
		q += ` AND id NOT IN (SELECT conflicting_id FROM conflicts WHERE resolved_at IS NULL)`
	}
	q += ` ORDER BY category ASC, confidence DESC, statement ASC`
	rows, err := conn.Query(q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	byCat := map[string][]struct {
		stmt string
		conf float64
	}{}
	var cats []string
	seen := map[string]bool{}
	for rows.Next() {
		var cat, stmt string
		var conf float64
		if err := rows.Scan(&cat, &stmt, &conf); err != nil {
			continue
		}
		if !seen[cat] {
			cats = append(cats, cat)
			seen[cat] = true
		}
		byCat[cat] = append(byCat[cat], struct {
			stmt string
			conf float64
		}{stmt, conf})
	}
	sort.Strings(cats)
	var b strings.Builder
	b.WriteString("# Taste\n\n")
	if len(cats) == 0 {
		b.WriteString("_No preferences learned yet._\n")
		return b.String(), nil
	}
	for _, cat := range cats {
		b.WriteString(fmt.Sprintf("## %s\n\n", cat))
		for _, p := range byCat[cat] {
			b.WriteString(fmt.Sprintf("- %s. Confidence: %.2f\n", strings.TrimSuffix(p.stmt, "."), p.conf))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func WriteFile(conn *sql.DB, path string, opts RenderOptions) error {
	content, err := Render(conn, opts)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
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
			}
		}
	}
	return imported, nil
}
