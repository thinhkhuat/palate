package skills

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/thinhkhuat/palate/internal/config"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	b.WriteString("version: \"0.1.0\"\n")
	b.WriteString("license: MIT\n")
	b.WriteString("compatibility: \"agentskills.io\"\n")
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# %s (Palate)\n\n", category))
	b.WriteString("Learned from repeated corrections. Review before installing.\n\n")
	for _, s := range stmts {
		b.WriteString(fmt.Sprintf("- %s\n", s))
	}
	b.WriteString("\n## Effectiveness\n\n")
	b.WriteString("Track usage: `palate skill stats <id>` increments success/use counts.\n")
	return b.String()
}

// Every helper below resolves its paths through the config so the layout
// (skill-candidates/, skills/, skill-stats.json under the Palate home) is
// defined in exactly one place.
func WriteCandidates(cfg config.Config, cands []Candidate) error {
	outDir := cfg.SkillCandidatesDir()
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

func InstallCandidate(cfg config.Config, id string) error {
	src := filepath.Join(cfg.SkillCandidatesDir(), id+".md")
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("candidate not found: %w", err)
	}
	destDir := filepath.Join(cfg.SkillsDir(), id)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "SKILL.md"), b, 0644)
}

type Effectiveness struct {
	SkillID   string `json:"skill_id"`
	Uses      int    `json:"uses"`
	Success   int    `json:"success"`
	UpdatedAt string `json:"updated_at"`
}

func StatsPath(cfg config.Config) string { return filepath.Join(cfg.Home, "skill-stats.json") }

func LoadStats(cfg config.Config) map[string]*Effectiveness {
	m := map[string]*Effectiveness{}
	b, err := os.ReadFile(StatsPath(cfg))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func BumpStat(cfg config.Config, skillID string, success bool) error {
	_ = os.MkdirAll(cfg.Home, 0700)
	m := LoadStats(cfg)
	e, ok := m[skillID]
	if !ok {
		e = &Effectiveness{SkillID: skillID}
		m[skillID] = e
	}
	e.Uses++
	if success {
		e.Success++
	}
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(StatsPath(cfg), b, 0644)
}

func Monitors(cfg config.Config) []string {
	m := LoadStats(cfg)
	var issues []string
	for id, e := range m {
		if e.Uses >= 10 && float64(e.Success)/float64(e.Uses) < 0.3 {
			issues = append(issues, fmt.Sprintf("%s: rot — success rate %.0f%% over %d uses", id, float64(e.Success)/float64(e.Uses)*100, e.Uses))
		}
		if e.Uses == 0 {
			issues = append(issues, fmt.Sprintf("%s: stale — never used", id))
		}
	}
	return issues
}
