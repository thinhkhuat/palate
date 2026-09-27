// Package consolidate is the dream pass: the batched, budget-aware review of
// the whole taste profile plus every changed candidate — the Command Code
// learn→taste loop's whole-store housekeeper. The profile (learn.SelectProfile's
// In rows) rides along with every batch; candidates (live rows not in the
// profile) are cut into batches of batchSize so the request never grows past
// the gateway's limit no matter how large the ledger gets. In distill.mode
// "auto" a batch's actions are applied (after a DB backup, through the events
// funnel so TASTE.md follows); in "review", or when a batch would touch too
// large a share of the ledger, they become proposals for `palate promote`.
// Two guards keep one bad reply from gutting the store: a row reinforced in
// the last 24h AND backed by ≥2 sessions is never retired, and the cumulative
// share of the live ledger touched by model actions is capped at 40% —
// downgrading only the batch that would cross it, never the whole pass.
// After the batches, a deterministic, code-only step retires stale one-off
// candidates (never STATED, never a profile member) with a logged, restorable
// reason. Every attempt — applied, proposed, a no-op, a busy lock, or an
// error — is recorded to state/consolidate.log (attempt.go), so a failure is
// never silent: the next SessionStart notice surfaces it.
package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/render"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

const (
	// batchSize bounds how many candidates one gateway call sees, alongside
	// the whole profile (req 1). Not a config key (plan.md's over-engineering
	// audit: a code-level constant, not an owner default).
	batchSize = 40

	// maxTouched is the cumulative share of the live ledger a pass's model
	// actions may touch before a batch is downgraded to proposals (req 4).
	maxTouched = 0.40

	// retireGuardWindow + retireGuardMinSessions: the narrowed 24h retire
	// shield (req 4) — it protects only a row reinforced within the window
	// AND backed by at least this many distinct sessions.
	retireGuardWindow      = 24 * time.Hour
	retireGuardMinSessions = 2

	passRetries    = 1
	proposalSource = "consolidate"

	// defaultConsolidateTimeout mirrors config.Consolidate.TimeoutSeconds'
	// default (req 10); used only if a caller builds a gateway without going
	// through BuildGateway (tests, direct construction).
	defaultConsolidateTimeout = 120 * time.Second
)

// errActionRefused marks an action ApplyAction declined to apply (a rewrite
// guard refusal, or two disagreeing STATED rows in a merge) — a Skipped
// outcome for Run's report, never an apply error.
var errActionRefused = errors.New("consolidate: action refused")

// Action is one instruction from the model.
type Action struct {
	Type       string   `json:"type"` // merge | rewrite | rescore | retire | project
	IDs        []string `json:"ids,omitempty"`
	ID         string   `json:"id,omitempty"`
	Statement  string   `json:"statement,omitempty"`
	Category   string   `json:"category,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// actionIDs returns every id an action names (ID and/or IDs), the set a
// guard or an apply step must validate against the live ledger.
func actionIDs(a Action) []string {
	ids := append([]string(nil), a.IDs...)
	if a.ID != "" {
		ids = append(ids, a.ID)
	}
	return ids
}

// RunOptions configures one Run call (req 1, 4, 11).
type RunOptions struct {
	// Rebuild makes candidates every live row (not just the delta since the
	// last applied pass) and disables the 40% guard — the CLI's
	// `palate consolidate --rebuild`, never set by a hook or config.
	Rebuild bool
	// Trigger names who called Run, carried onto the attempt record.
	Trigger string
	// OnBatch runs between batches — the caller's seam to refresh the learn
	// lock's mtime so a long, multi-batch pass is never judged stale.
	OnBatch func()
}

// Report is what one Run call did.
type Report struct {
	Rows      int // candidates considered (batches, not the whole ledger)
	Batches   int // gateway calls completed
	Actions   int // actions kept after guards, applied + proposed
	Applied   int
	Proposed  int
	Skipped   int
	Retired   int    // stale-candidate rows retired (code-only step, req 6)
	Mode      string // auto | review | review(guard)
	Outcome   string // applied | proposals | nothing-to-do | error (req 8)
	Watermark int64
}

// Due reports how many events have landed since the last pass (of any mode —
// req 7) and whether the auto-fire threshold is reached.
func Due(conn *sql.DB, cfg config.Config) (since int64, every int, due bool) {
	var last, max sql.NullInt64
	_ = conn.QueryRow(`SELECT MAX(last_event_id) FROM consolidations`).Scan(&last)
	_ = conn.QueryRow(`SELECT MAX(id) FROM events`).Scan(&max)
	since = max.Int64 - last.Int64
	every = cfg.Consolidate.EveryEvents
	return since, every, every > 0 && since >= int64(every)
}

// MaxEventID is the events table's current high-water mark, used both by
// watermark() here and by the CLI to stamp a skipped-lock attempt's event_id
// (req 8) without duplicating the query.
func MaxEventID(conn *sql.DB) int64 {
	var max sql.NullInt64
	_ = conn.QueryRow(`SELECT MAX(id) FROM events`).Scan(&max)
	return max.Int64
}

// BuildGateway wraps distill.NewGatewayFromEnv with the dream pass's own,
// longer timeout (req 10): cfg.Consolidate.TimeoutSeconds (default 120) on
// both the HTTP client and the Gateway's own deadline. PALATE_LLM_TIMEOUT /
// cfg.LLMTimeout() still governs the distill and judge paths untouched.
func BuildGateway(cfg config.Config) (*distill.Gateway, bool) {
	gw, ok := distill.NewGatewayFromEnv(cfg)
	if !ok {
		return nil, false
	}
	to := defaultConsolidateTimeout
	if cfg.Consolidate.TimeoutSeconds > 0 {
		to = time.Duration(cfg.Consolidate.TimeoutSeconds) * time.Second
	}
	gw.Timeout = to
	if gw.HTTPClient != nil {
		gw.HTTPClient.Timeout = to
	}
	return gw, true
}

// Run performs one dream pass. gw nil → error (callers check LLMConfigured
// first, or BuildGateway already did).
func Run(ctx context.Context, conn *sql.DB, cfg config.Config, gw *distill.Gateway, opts RunOptions) (Report, error) {
	var rep Report
	if gw == nil {
		rep.Outcome = "error"
		return rep, fmt.Errorf("consolidate: no LLM gateway configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL)")
	}

	budget := render.ProfileBudget(cfg)
	profile, profileTokens, candidates, err := selectInput(conn, cfg, budget, opts.Rebuild)
	if err != nil {
		rep.Outcome = "error"
		return rep, err
	}
	rep.Rows = len(candidates)

	mode := cfg.Distill.Mode
	if mode != "review" {
		mode = "auto"
	}

	if len(candidates) == 0 {
		changed, cerr := profileChangedSinceLastApplied(conn)
		if cerr != nil {
			rep.Outcome = "error"
			return rep, cerr
		}
		if !changed {
			rep.Mode = mode
			rep.Outcome = "nothing-to-do"
			rep.Watermark = watermark(conn, 0, mode)
			return rep, nil
		}
	}

	var batches [][]learn.Preference
	if len(candidates) == 0 {
		batches = [][]learn.Preference{nil} // profile alone, one call
	} else {
		for i := 0; i < len(candidates); i += batchSize {
			end := i + batchSize
			if end > len(candidates) {
				end = len(candidates)
			}
			batches = append(batches, candidates[i:end])
		}
	}

	rulebookBlock, _ := learn.LoadRulebook(cfg)
	sessions, err := learn.SessionCounts(conn)
	if err != nil {
		rep.Outcome = "error"
		return rep, err
	}
	conflicts, err := openConflictPairs(conn)
	if err != nil {
		rep.Outcome = "error"
		return rep, err
	}
	byID := map[string]learn.Preference{}
	for _, p := range profile {
		byID[p.ID] = p
	}
	for _, p := range candidates {
		byID[p.ID] = p
	}

	var ledgerTotal int
	_ = conn.QueryRow(`SELECT count(*) FROM preferences WHERE valid_until IS NULL`).Scan(&ledgerTotal)

	now := time.Now().UTC()
	touchedTotal := map[string]bool{}
	backedUp := false
	anyApplied, anyGuardDowngrade := false, false

	for _, batch := range batches {
		userPrompt := BuildUserPrompt(profile, batch, rulebookBlock, budget, profileTokens, sessions, conflicts)
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
				case "merge", "rewrite", "rescore", "retire", "project":
				default:
					return fmt.Errorf("action %d has unknown type %q", i, a.Type)
				}
			}
			actions = env.Actions
			return nil
		}
		if _, err := gw.Complete(ctx, SystemPrompt, userPrompt, passRetries, validate); err != nil {
			rep.Outcome = "error"
			return rep, err
		}
		rep.Batches++

		var kept []Action
		localTouched := map[string]bool{}
		for _, a := range actions {
			ids := actionIDs(a)
			valid := len(ids) > 0
			for _, id := range ids {
				p, ok := byID[id]
				if !ok {
					valid = false
					break
				}
				if a.Type == "retire" && shielded(p, sessions[id], now) {
					valid = false
					break
				}
			}
			if !valid {
				rep.Skipped++
				storage.BumpDrop(conn, "consolidate_skipped_guard")
				continue
			}
			for _, id := range ids {
				localTouched[id] = true
			}
			kept = append(kept, a)
		}
		rep.Actions += len(kept)

		batchMode := mode
		if !opts.Rebuild && mode == "auto" && ledgerTotal > 0 {
			projected := len(touchedTotal)
			for id := range localTouched {
				if !touchedTotal[id] {
					projected++
				}
			}
			if float64(projected) > maxTouched*float64(ledgerTotal) {
				batchMode = "review(guard)"
				anyGuardDowngrade = true
				fmt.Fprintf(os.Stderr, "palate: consolidation batch would touch %d of %d rows (> %.0f%%) — written as proposals instead\n", projected, ledgerTotal, maxTouched*100)
			}
		}
		for id := range localTouched {
			touchedTotal[id] = true
		}

		if batchMode == "auto" && len(kept) > 0 {
			if !backedUp {
				if bak, err := storage.BackupBefore(cfg.DBPath, cfg.BackupDir(), "consolidate"); err != nil {
					rep.Outcome = "error"
					return rep, fmt.Errorf("backup before consolidate: %w", err)
				} else if bak != "" {
					fmt.Fprintln(os.Stderr, "backup:", bak)
				}
				backedUp = true
			}
			for _, a := range kept {
				if err := ApplyAction(conn, cfg, a); err != nil {
					rep.Skipped++
					if !errors.Is(err, errActionRefused) {
						storage.BumpDrop(conn, "consolidate_apply_error")
					}
					continue
				}
				rep.Applied++
				anyApplied = true
				storage.BumpHit(conn, "consolidate_applied")
			}
		} else if len(kept) > 0 {
			for _, a := range kept {
				b, _ := json.Marshal(a)
				if pendingProposalExists(conn, string(b)) {
					continue
				}
				if _, err := learn.CreateProposalFrom(conn, string(b), a.Category, proposalSource); err != nil {
					rep.Skipped++
					continue
				}
				rep.Proposed++
				storage.BumpHit(conn, "consolidate_proposed")
			}
		}

		if opts.OnBatch != nil {
			opts.OnBatch()
		}
	}

	if mode == "auto" {
		retired, err := retireStaleCandidates(conn, cfg, profile, candidates, sessions, now)
		if err != nil {
			rep.Outcome = "error"
			return rep, err
		}
		rep.Retired = retired
	}

	switch {
	case mode == "review":
		rep.Mode = "review"
	case anyGuardDowngrade && !anyApplied:
		rep.Mode = "review(guard)"
	default:
		rep.Mode = "auto"
	}
	switch {
	case anyApplied || rep.Retired > 0:
		rep.Outcome = "applied"
	case rep.Proposed > 0:
		rep.Outcome = "proposals"
	default:
		rep.Outcome = "nothing-to-do"
	}
	rep.Watermark = watermark(conn, rep.Applied+rep.Proposed, rep.Mode)
	return rep, nil
}

// shielded is the narrowed 24h retire guard (req 4): only a row reinforced
// within the window AND backed by at least retireGuardMinSessions is
// protected — a one-off candidate reinforced an hour ago is still retirable.
func shielded(p learn.Preference, sessionCount int, now time.Time) bool {
	return sessionCount >= retireGuardMinSessions && recentlyReinforced(p, now)
}

func recentlyReinforced(p learn.Preference, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, p.LastReinforcedAt)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", p.LastReinforcedAt)
		if err != nil {
			return false
		}
	}
	return now.Sub(t) < retireGuardWindow
}

// selectInput implements req 1: the profile (SelectProfile's In rows, plus
// its token cost) and the candidate batch — live rows not in the profile,
// restricted to the delta since the last applied (mode=auto) pass unless
// Rebuild is set. Rulebook-tagged rows and rows named by a still-pending
// consolidate proposal ride along regardless of the delta, so a rulebook
// restatement always gets a chance to be retired and a guard-downgraded
// batch is re-sent rather than lost (req 4, red-team H2).
func selectInput(conn *sql.DB, cfg config.Config, budget int, rebuild bool) (profile []learn.Preference, profileTokens int, candidates []learn.Preference, err error) {
	p, err := learn.SelectProfile(conn, cfg, budget)
	if err != nil {
		return nil, 0, nil, err
	}
	profile, profileTokens = p.In, p.Tokens
	inProfile := make(map[string]bool, len(profile))
	for _, row := range profile {
		inProfile[row.ID] = true
	}

	all, err := learn.ListPreferences(conn, true) // every live row, conflict losers included
	if err != nil {
		return nil, 0, nil, err
	}

	var basis string
	if !rebuild {
		_ = conn.QueryRow(`SELECT MAX(at) FROM consolidations WHERE mode='auto'`).Scan(&basis)
	}
	pending, err := pendingProposalIDs(conn)
	if err != nil {
		return nil, 0, nil, err
	}

	for _, row := range all {
		if inProfile[row.ID] {
			continue
		}
		if rebuild || row.CreatedAt > basis || row.LastReinforcedAt > basis || learn.HasRulebookOverlap(row.Provenance) || pending[row.ID] {
			candidates = append(candidates, row)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		return candidates[i].ID < candidates[j].ID
	})
	return profile, profileTokens, candidates, nil
}

// profileChangedSinceLastApplied reports whether a profile_add/profile_cut
// event landed after the last mode=auto consolidations row — with zero
// candidates, that is the only reason left to still send the profile alone
// (req 1).
func profileChangedSinceLastApplied(conn *sql.DB) (bool, error) {
	var basis string
	_ = conn.QueryRow(`SELECT MAX(at) FROM consolidations WHERE mode='auto'`).Scan(&basis)
	var n int
	err := conn.QueryRow(`SELECT count(*) FROM events WHERE event_type IN ('profile_add','profile_cut') AND created_at > ?`, basis).Scan(&n)
	return n > 0, err
}

// pendingProposalIDs returns every id named by a still-pending consolidate
// proposal (its stored Action's ID/IDs), so a guard-downgraded batch's rows
// stay candidates on the next pass instead of falling out of the delta.
func pendingProposalIDs(conn *sql.DB) (map[string]bool, error) {
	rows, err := conn.Query(`SELECT content FROM proposals WHERE source=? AND status='pending'`, proposalSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var content string
		if rows.Scan(&content) != nil {
			continue
		}
		var a Action
		if json.Unmarshal([]byte(content), &a) != nil {
			continue
		}
		for _, id := range actionIDs(a) {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// pendingProposalExists reports whether an identical consolidate proposal is
// already pending, so a repeatedly guard-downgraded batch never files the
// same action twice (req 7).
func pendingProposalExists(conn *sql.DB, content string) bool {
	var n int
	_ = conn.QueryRow(`SELECT count(*) FROM proposals WHERE source=? AND status='pending' AND content=?`, proposalSource, content).Scan(&n)
	return n > 0
}

// openConflictPairs returns, for every row on either side of an unresolved
// conflict, the id of the row it conflicts with — the "conflicts-with"
// profile-line marker (req 1).
func openConflictPairs(conn *sql.DB) (map[string]string, error) {
	rows, err := conn.Query(`SELECT preference_id, conflicting_id FROM conflicts WHERE resolved_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, b string
		if rows.Scan(&a, &b) != nil {
			continue
		}
		out[a] = b
		out[b] = a
	}
	return out, rows.Err()
}

// retireStaleCandidates is the deterministic, no-LLM step (req 6): a live,
// non-STATED, non-profile-member row backed by exactly one session, created
// more than cfg.Consolidate.StaleCandidateDays ago (0 disables it) and not
// reinforced in the last 24h, is retired with a restorable reason. Exempt
// from the 40% guard — code decided it, not the model.
func retireStaleCandidates(conn *sql.DB, cfg config.Config, profile, candidates []learn.Preference, sessions map[string]int, now time.Time) (int, error) {
	days := cfg.Consolidate.StaleCandidateDays
	if days <= 0 {
		return 0, nil
	}
	inProfile := make(map[string]bool, len(profile))
	for _, p := range profile {
		inProfile[p.ID] = true
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	n := 0
	for _, p := range candidates {
		if inProfile[p.ID] || p.TrustTier == "STATED" {
			continue
		}
		if sessions[p.ID] != 1 {
			continue
		}
		created, err := time.Parse(time.RFC3339, p.CreatedAt)
		if err != nil || created.After(cutoff) {
			continue
		}
		if recentlyReinforced(p, now) {
			continue
		}
		nowStr := now.Format(time.RFC3339)
		if _, err := conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=? AND valid_until IS NULL`, nowStr, p.ID); err != nil {
			return n, err
		}
		reason := fmt.Sprintf("stale_candidate: 1 session, created %s, not repeated within %d days", p.CreatedAt, days)
		if err := storage.AppendEvent(conn, "consolidate_retire", fmt.Sprintf(`{"id":%q,"reason":%q}`, p.ID, reason)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func watermark(conn *sql.DB, actions int, mode string) int64 {
	max := MaxEventID(conn)
	_, _ = conn.Exec(`INSERT INTO consolidations(last_event_id, actions, mode) VALUES(?,?,?)`, max, actions, mode)
	return max
}

// scanPreference reads the minimum an ApplyAction case needs to gate a
// rewrite (id, statement, trust tier).
func scanPreference(conn *sql.DB, id string) (learn.Preference, error) {
	var p learn.Preference
	p.ID = id
	err := conn.QueryRow(`SELECT statement, category, confidence, trust_tier FROM preferences WHERE id=? AND valid_until IS NULL`, id).Scan(&p.Statement, &p.Category, &p.Confidence, &p.TrustTier)
	return p, err
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
		p, err := scanPreference(conn, a.ID)
		if err != nil {
			return err
		}
		if allowed, reason := learn.RewriteAllowed(conn, p, a.Statement, true); !allowed {
			storage.BumpDrop(conn, "consolidate_rewrite_refused")
			fmt.Fprintf(os.Stderr, "palate: consolidate: rewrite of %s refused: %s\n", a.ID, reason)
			return errActionRefused
		}
		// A rewrite may also re-file the row: an empty category keeps the current one.
		if _, err := conn.Exec(`UPDATE preferences SET statement=?, category=COALESCE(NULLIF(?, ''), category) WHERE id=?`, a.Statement, strings.TrimSpace(a.Category), a.ID); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_rewrite", fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"category":%q,"reason":%q}`, a.ID, p.Statement, a.Statement, strings.TrimSpace(a.Category), a.Reason))
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
		if err := resolveConflictsFor(conn, a.ID, now); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_retire", fmt.Sprintf(`{"id":%q,"reason":%q}`, a.ID, a.Reason))
	case "project":
		if a.ID == "" {
			return fmt.Errorf("project needs id")
		}
		if err := tagProjectSpecific(conn, a.ID); err != nil {
			return err
		}
		return storage.AppendEvent(conn, "consolidate_project", fmt.Sprintf(`{"id":%q,"reason":%q}`, a.ID, a.Reason))
	case "merge":
		if len(a.IDs) < 2 || strings.TrimSpace(a.Statement) == "" {
			return fmt.Errorf("merge needs ≥2 ids and a statement")
		}
		var rows []learn.Preference
		for _, id := range a.IDs {
			if p, err := scanPreference(conn, id); err == nil {
				p.ID = id
				rows = append(rows, p)
			}
		}
		if len(rows) == 0 {
			return fmt.Errorf("merge: no live rows among %v", a.IDs)
		}
		var stated []learn.Preference
		for _, p := range rows {
			if p.TrustTier == "STATED" {
				stated = append(stated, p)
			}
		}
		statement := a.Statement
		pool := rows
		if len(stated) > 0 {
			for _, s := range stated[1:] {
				if s.Statement != stated[0].Statement {
					fmt.Fprintf(os.Stderr, "palate: consolidate: merge of %v refused: two STATED rows disagree\n", a.IDs)
					return errActionRefused
				}
			}
			statement = stated[0].Statement // STATED text is never rewritten, even by a merge
			pool = stated
		}
		var keep string
		var best float64 = -1
		for _, p := range pool {
			if p.Confidence > best {
				keep, best = p.ID, p.Confidence
			}
		}
		c := learn.ClampConfidence(a.Confidence, cfg.CFloor)
		if a.Confidence == 0 {
			c = best
		}
		cat := a.Category
		if cat == "" {
			for _, p := range rows {
				if p.ID == keep {
					cat = p.Category
				}
			}
		}
		merged, _ := json.Marshal(a.IDs)
		if _, err := conn.Exec(`UPDATE preferences SET statement=?, category=?, confidence=?, last_reinforced_at=?, provenance=json_insert(provenance, '$[#]', ?) WHERE id=?`,
			statement, cat, c, now, "merged_from:"+string(merged), keep); err != nil {
			return err
		}
		for _, id := range a.IDs {
			if id == keep {
				continue
			}
			if _, err := conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=? AND valid_until IS NULL`, now, id); err != nil {
				return err
			}
			if err := resolveConflictsFor(conn, id, now); err != nil {
				return err
			}
		}
		return storage.AppendEvent(conn, "consolidate_merge", fmt.Sprintf(`{"keep":%q,"ids":%s,"statement":%q,"c":%f,"reason":%q}`, keep, string(merged), statement, c, a.Reason))
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

// resolveConflictsFor closes every open conflict naming id — a retire or a
// merge-close resolves the contradiction it settles, so the winning side
// stops rendering as still-conflicted (req 5; consolidate.go:233-275's old
// gap).
func resolveConflictsFor(conn *sql.DB, id, now string) error {
	_, err := conn.Exec(`UPDATE conflicts SET resolved_at=? WHERE resolved_at IS NULL AND (preference_id=? OR conflicting_id=?)`, now, id, id)
	return err
}

// tagProjectSpecific appends learn.TagProjectSpecific to id's provenance,
// mirroring how RulebookOverlap is tagged (preferences.go:635-639) — a
// no-op when the tag is already present.
func tagProjectSpecific(conn *sql.DB, id string) error {
	var prov string
	if err := conn.QueryRow(`SELECT provenance FROM preferences WHERE id=?`, id).Scan(&prov); err != nil {
		return err
	}
	if learn.HasProjectSpecific(prov) {
		return nil
	}
	_, err := conn.Exec(`UPDATE preferences SET provenance=json_insert(provenance, '$[#]', ?) WHERE id=?`, learn.TagProjectSpecific, id)
	return err
}

// ApplyProposal runs a consolidation proposal's stored action.
func ApplyProposal(conn *sql.DB, cfg config.Config, content string) error {
	var a Action
	if err := json.Unmarshal([]byte(content), &a); err != nil {
		return fmt.Errorf("proposal is not a consolidation action: %w", err)
	}
	if err := ApplyAction(conn, cfg, a); err != nil {
		if errors.Is(err, errActionRefused) {
			return nil // a promoted-but-now-refused action is a no-op, not a failure
		}
		return err
	}
	return nil
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

// BuildUserPrompt lists the profile (in full, every batch) and the batch's
// candidates, censored, one row per line, each carrying the markers req 1
// asks for (stated, sessions=N, conflicts-with=<id>). The curated rulebook,
// when configured, rides along so restatements can be retired even though
// they predate the rulebook feature.
func BuildUserPrompt(profile, batch []learn.Preference, rulebookBlock string, budget, profileTokens int, sessions map[string]int, conflicts map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<budget>\ntarget: %d tokens (estimated). current profile size: %d tokens.\n</budget>\n", budget, profileTokens)
	b.WriteString("<profile>\n")
	for _, p := range profile {
		writeConsolidateRow(&b, p, sessions, conflicts)
	}
	b.WriteString("</profile>\n<candidates>\n")
	for _, p := range batch {
		writeConsolidateRow(&b, p, sessions, conflicts)
	}
	b.WriteString("</candidates>")
	if rulebookBlock != "" {
		b.WriteString("\n\n## CURATED RULEBOOK (authoritative, always loaded — retire rows that merely restate one of its rules)\n")
		b.WriteString(rulebookBlock)
	}
	return b.String()
}

func writeConsolidateRow(b *strings.Builder, p learn.Preference, sessions map[string]int, conflicts map[string]string) {
	marker := fmt.Sprintf(" | sessions=%d", sessions[p.ID])
	if learn.HasRulebookOverlap(p.Provenance) {
		marker += " | rulebook"
	}
	if p.TrustTier == "STATED" {
		marker += " | stated"
	}
	if other, ok := conflicts[p.ID]; ok {
		marker += " | conflicts-with=" + other
	}
	fmt.Fprintf(b, "- id=%s | [%s] %s | confidence %.2f | reinforced %d× | created %s | last %s | %s/%s%s\n",
		p.ID, p.Category, safety.Censor(p.Statement), p.Confidence, p.ReinforcementCount, p.CreatedAt, p.LastReinforcedAt, p.Source, p.TrustTier, marker)
}
