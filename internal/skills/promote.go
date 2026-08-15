package skills

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Candidate struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Content  string `json:"content"`
	Count    int    `json:"count"`
	Hash     string `json:"hash"`
}

func Cluster(db *sql.DB, threshold int) ([]Candidate, error) {
	if threshold <= 0 {
		threshold = 3
	}
	rows, err := db.Query(`
		SELECT category, statement, reinforcement_count FROM preferences
		WHERE valid_until IS NULL AND reinforcement_count >= 2
		ORDER BY category, statement`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byCat := map[string][]string{}
	for rows.Next() {
		var cat, stmt string
		var rc int
		if err := rows.Scan(&cat, &stmt, &rc); err != nil {
			continue
		}
		byCat[cat] = append(byCat[cat], stmt)
	}
	var out []Candidate
	for cat, stmts := range byCat {
		if len(stmts) < threshold {
			continue
		}
		sort.Strings(stmts)
		h := sha256.Sum256([]byte(strings.Join(stmts, "|")))
		hash := fmt.Sprintf("%x", h[:8])
		content := renderSkill(cat, stmts)
		out = append(out, Candidate{
			ID:       fmt.Sprintf("%s-%s", strings.ToLower(strings.ReplaceAll(cat, " ", "-")), hash),
			Category: cat,
			Content:  content,
			Count:    len(stmts),
			Hash:     hash,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func renderSkill(category string, stmts []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("name: palate-%s\n", strings.ToLower(strings.ReplaceAll(category, " ", "-"))))
	b.WriteString(fmt.Sprintf("description: Palate-learned skill for %s (%d preferences)\n", category, len(stmts)))
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# %s (Palate)\n\n", category))
	b.WriteString("Learned from repeated corrections. Review before installing.\n\n")
	for _, s := range stmts {
		b.WriteString(fmt.Sprintf("- %s\n", s))
	}
	return b.String()
}

func WriteCandidates(dir string, cands []Candidate) error {
	outDir := filepath.Join(dir, ".palate", "skill-candidates")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return err
	}
	for _, c := range cands {
		path := filepath.Join(outDir, c.ID+".md")
		existing, _ := os.ReadFile(path)
		if string(existing) == c.Content {
			continue
		}
		if err := os.WriteFile(path, []byte(c.Content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func InstallCandidate(dir, id string) error {
	src := filepath.Join(dir, ".palate", "skill-candidates", id+".md")
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("candidate not found: %w", err)
	}
	destDir := filepath.Join(dir, ".palate", "skills", id)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "SKILL.md"), b, 0644)
}
