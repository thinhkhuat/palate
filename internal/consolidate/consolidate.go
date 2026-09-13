// Package consolidate is the whole-profile review pass: the gateway reads
// every active preference and returns merge / rewrite / rescore / retire
// actions — the Command Code learn→taste loop. In distill.mode "auto" the
// actions are applied (after a DB backup, through the events funnel so
// TASTE.md follows); in "review" each becomes a proposal for `palate promote`.
// Two guards keep one bad reply from gutting the store: a row reinforced in
// the last 24 h is never retired, and a pass touching more than 40 % of the
// rows is downgraded to proposals whatever the mode.
package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

const (
	maxRows        = 200
	maxTouched     = 0.40
	retireGuard    = 24 * time.Hour
	passRetries    = 1
	proposalSource = "consolidate"
)

// Action is one instruction from the model.
type Action struct {
	Type       string   `json:"type"` // merge | rewrite | rescore | retire
	IDs        []string `json:"ids,omitempty"`
	ID         string   `json:"id,omitempty"`
	Statement  string   `json:"statement,omitempty"`
	Category   string   `json:"category,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// Report is what a pass did.
type Report struct {
	Rows      int
	Actions   int
	Applied   int
	Proposed  int
	Skipped   int
	Mode      string // auto | review | review(guard)
	Watermark int64
}

// Due reports how many events have landed since the last pass and whether
// the auto-fire threshold is reached.
func Due(conn *sql.DB, cfg config.Config) (since int64, every int, due bool) {
	var last, max sql.NullInt64
	_ = conn.QueryRow(`SELECT MAX(last_event_id) FROM consolidations`).Scan(&last)
	_ = conn.QueryRow(`SELECT MAX(id) FROM events`).Scan(&max)
	since = max.Int64 - last.Int64
	every = cfg.Consolidate.EveryEvents
	return since, every, every > 0 && since >= int64(every)
}

// Run performs one pass. gw nil → error (callers check LLMConfigured first).
func Run(ctx context.Context, conn *sql.DB, cfg config.Config, gw *distill.Gateway) (Report, error) {
	var rep Report
	if gw == nil {
		return rep, fmt.Errorf("consolidate: no LLM gateway configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL)")
	}
	rows, err := learn.ListPreferences(conn, false)
	if err != nil {
		return rep, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Confidence > rows[j].Confidence })
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	rep.Rows = len(rows)
	if len(rows) == 0 {
		rep.Watermark = watermark(conn, 0, "auto")
		return rep, nil
	}
	byID := map[string]learn.Preference{}
	for _, p := range rows {
		byID[p.ID] = p
	}

	var actions []Action
	validate := func(raw string) error {
		var env struct {
			Actions []Action `json:"actions"`
		}
		if err := json.Unmarshal([]byte(stripFence(raw)), &env); err != nil {
			return fmt.Errorf("reply is not {\"actions\":[…]}: %w", err)
		}
		for i, a := range env.Actions {
			switch a.Type {
			case "merge", "rewrite", "rescore", "retire":
			default:
				return fmt.Errorf("action %d has unknown type %q", i, a.Type)
			}
		}
		actions = env.Actions
		return nil
	}
	if _, err := gw.Complete(ctx, SystemPrompt, BuildUserPrompt(rows), passRetries, validate); err != nil {
		return rep, err
	}

	// Guards.
	now := time.Now().UTC()
	touched := map[string]bool{}
	var kept []Action
	for _, a := range actions {
		ids := a.IDs
		if a.ID != "" {
			ids = append(ids, a.ID)
		}
		valid := true
		for _, id := range ids {
			p, ok := byID[id]
			if !ok {
				valid = false
				break
			}
			if a.Type == "retire" && recentlyReinforced(p, now) {
				valid = false
				break
			}
		}
		if !valid || len(ids) == 0 {
			rep.Skipped++
			storage.BumpDrop(conn, "consolidate_skipped_guard")
			continue
		}
		for _, id := range ids {
			touched[id] = true
		}
		kept = append(kept, a)
	}
	rep.Actions = len(kept)
	mode := cfg.Distill.Mode
	if mode != "review" {
		mode = "auto"
	}
	if float64(len(touched)) > maxTouched*float64(len(rows)) && mode == "auto" {
		fmt.Fprintf(os.Stderr, "palate: consolidation would touch %d of %d rows (> %.0f%%) — written as proposals instead\n", len(touched), len(rows), maxTouched*100)
		mode = "review(guard)"
	}
	rep.Mode = mode

	if mode == "auto" && len(kept) > 0 {
		if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "consolidate"); err != nil {
			return rep, fmt.Errorf("backup before consolidate: %w", err)
		} else if bak != "" {
			fmt.Fprintln(os.Stderr, "backup:", bak)
		}
		for _, a := range kept {
			if err := ApplyAction(conn, cfg, a); err != nil {
				rep.Skipped++
				storage.BumpDrop(conn, "consolidate_apply_error")
				continue
			}
			rep.Applied++
			storage.BumpHit(conn, "consolidate_applied")
		}
	} else {
		for _, a := range kept {
			b, _ := json.Marshal(a)
			if _, err := learn.CreateProposalFrom(conn, string(b), a.Category, proposalSource); err != nil {
				rep.Skipped++
				continue
			}
			rep.Proposed++
			storage.BumpHit(conn, "consolidate_proposed")
		}
	}
	rep.Watermark = watermark(conn, rep.Applied+rep.Proposed, mode)
	return rep, nil
}

func recentlyReinforced(p learn.Preference, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, p.LastReinforcedAt)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", p.LastReinforcedAt)
		if err != nil {
			return false
		}
	}
	return now.Sub(t) < retireGuard
}

func watermark(conn *sql.DB, actions int, mode string) int64 {
	var max sql.NullInt64
	_ = conn.QueryRow(`SELECT MAX(id) FROM events`).Scan(&max)
	_, _ = conn.Exec(`INSERT INTO consolidations(last_event_id, actions, mode) VALUES(?,?,?)`, max.Int64, actions, mode)
	return max.Int64
}

// ApplyAction executes one action through the events funnel. Also used by
// `palate promote` for a consolidation proposal.
func ApplyAction(conn *sql.DB, cfg config.Config, a Action) error {
	now := time.Now().UTC().Format(time.RFC3339)
	switch a.Type {
	case "rewrite":
		if a.ID == "" || strings.TrimSpace(a.Statement) == "" {
			return fmt.Errorf("rewrite needs id and statement")
		}
		var from string
		if err := conn.QueryRow(`SELECT statement FROM preferences WHERE id=? AND valid_until IS NULL`, a.ID).Scan(&from); err != nil {
			return err
		}
		if _, err := conn.Exec(`UPDATE preferences SET statement=? WHERE id=?`, a.Statement, a.ID); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_rewrite", fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"reason":%q}`, a.ID, from, a.Statement, a.Reason))
	case "rescore":
		if a.ID == "" {
			return fmt.Errorf("rescore needs id")
		}
		c := learn.ClampConfidence(a.Confidence, cfg.CFloor)
		if _, err := conn.Exec(`UPDATE preferences SET confidence=?, last_reinforced_at=? WHERE id=? AND valid_until IS NULL`, c, now, a.ID); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_rescore", fmt.Sprintf(`{"id":%q,"c":%f,"reason":%q}`, a.ID, c, a.Reason))
	case "retire":
		if a.ID == "" {
			return fmt.Errorf("retire needs id")
		}
		if _, err := conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=? AND valid_until IS NULL`, now, a.ID); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_retire", fmt.Sprintf(`{"id":%q,"reason":%q}`, a.ID, a.Reason))
	case "merge":
		if len(a.IDs) < 2 || strings.TrimSpace(a.Statement) == "" {
			return fmt.Errorf("merge needs ≥2 ids and a statement")
		}
		// keep the highest-confidence survivor, close the rest
		var keep string
		var best float64 = -1
		for _, id := range a.IDs {
			var c float64
			if err := conn.QueryRow(`SELECT confidence FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&c); err == nil && c > best {
				keep, best = id, c
			}
		}
		if keep == "" {
			return fmt.Errorf("merge: no live rows among %v", a.IDs)
		}
		c := learn.ClampConfidence(a.Confidence, cfg.CFloor)
		if a.Confidence == 0 {
			c = best
		}
		cat := a.Category
		if cat == "" {
			_ = conn.QueryRow(`SELECT category FROM preferences WHERE id=?`, keep).Scan(&cat)
		}
		merged, _ := json.Marshal(a.IDs)
		if _, err := conn.Exec(`UPDATE preferences SET statement=?, category=?, confidence=?, last_reinforced_at=?, provenance=json_insert(provenance, '$[#]', ?) WHERE id=?`,
			a.Statement, cat, c, now, "merged_from:"+string(merged), keep); err != nil {
			return err
		}
		for _, id := range a.IDs {
			if id != keep {
				_, _ = conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=? AND valid_until IS NULL`, now, id)
			}
		}
		return storage.AppendEvent(conn, "consolidate_merge", fmt.Sprintf(`{"keep":%q,"ids":%s,"statement":%q,"c":%f,"reason":%q}`, keep, string(merged), a.Statement, c, a.Reason))
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

// ApplyProposal runs a consolidation proposal's stored action.
func ApplyProposal(conn *sql.DB, cfg config.Config, content string) error {
	var a Action
	if err := json.Unmarshal([]byte(content), &a); err != nil {
		return fmt.Errorf("proposal is not a consolidation action: %w", err)
	}
	return ApplyAction(conn, cfg, a)
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

// BuildUserPrompt lists the profile, censored, one row per line.
func BuildUserPrompt(rows []learn.Preference) string {
	var b strings.Builder
	b.WriteString("<profile>\n")
	for _, p := range rows {
		fmt.Fprintf(&b, "- id=%s | [%s] %s | confidence %.2f | reinforced %d× | created %s | last %s | %s/%s\n",
			p.ID, p.Category, safety.Censor(p.Statement), p.Confidence, p.ReinforcementCount, p.CreatedAt, p.LastReinforcedAt, p.Source, p.TrustTier)
	}
	b.WriteString("</profile>")
	return b.String()
}
