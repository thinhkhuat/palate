// Package judge decides, for one explicit write, what Palate should store:
// the statement wording, its category, its confidence, and how it relates to
// the rows already in that category (new / same / refines / contradicts).
//
// Two implementations share one seam. LLMJudge asks the configured gateway —
// the "taste brain", the same role Command Code's own model plays for its
// taste file. HeuristicJudge reproduces the pre-0.3 behaviour exactly (keyword
// category, weight×0.5 confidence, exact/contradiction/near-duplicate
// matching) and is the fallback whenever no gateway is configured, the judge
// is switched off, or a call fails.
package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// Input is one candidate write.
type Input struct {
	Statement string
	Signal    learn.SignalType
	// Category is a hint; "" lets the judge decide.
	Category string
	// Confidence is the caller's evidence-based score when it has one (the
	// distiller saw the transcript; the judge sees only the wording). 0 means
	// none: the judge scores from the wording alone.
	Confidence float64
	// RulebookOverlap is the caller's own flag (the distiller saw the
	// rulebook too and may have tagged the statement); merged with the LLM
	// verdict's own flag in Apply.
	RulebookOverlap bool
	// ProjectSpecific is the caller's own flag that the statement describes
	// one project's own features, files, components or requirements rather
	// than how the user works (phase-03 req 3); merged with the LLM
	// verdict's own flag in Apply, the same way RulebookOverlap is.
	ProjectSpecific bool
	// Session identifies the session that produced this candidate (phase-03
	// req 1: the harness plus a short hash of the transcript path, or a git
	// window's own key). Forwarded into Signal.Session so the create/
	// reinforce event it produces carries it. Empty when the caller does
	// not know one (every explicit CLI/MCP write today).
	Session string
}

// Judge assesses a candidate write against the store.
type Judge interface {
	Assess(ctx context.Context, conn *sql.DB, cfg config.Config, in Input) (learn.Assessment, error)
	Name() string
}

// For picks the judge for this process: the gateway when one is configured
// and judge.enabled is on, the heuristics otherwise.
func For(cfg config.Config) Judge {
	if cfg.JudgeIsLLM() {
		if gw, ok := distill.NewGatewayFromEnv(cfg); ok {
			return &LLMJudge{Gateway: gw, Model: cfg.LLM.Model}
		}
	}
	return HeuristicJudge{}
}

// Mode is the one-line description doctor prints.
func Mode(cfg config.Config) string {
	switch {
	case !cfg.Judge.Enabled:
		return "heuristic (PALATE_JUDGE=0)"
	case !cfg.LLMConfigured():
		return "heuristic (no gateway)"
	default:
		// The model name comes from PALATE_LLM_MODEL (the LLM config is
		// env-only), so the source label makes doctor's staleness visible:
		// a process started before harness-env.sh changed prints its own
		// stale env here, by design.
		return "llm (" + cfg.LLM.Model + " via PALATE_LLM_MODEL)"
	}
}

// Apply is the one call every write path makes: judge, then store. A gateway
// failure degrades to the heuristic verdict with one stderr line and a
// counter, never an error — an explicit write must always land.
func Apply(ctx context.Context, conn *sql.DB, cfg config.Config, in Input, evidence string) (*learn.Preference, learn.Assessment, error) {
	j := For(cfg)
	a, err := j.Assess(ctx, conn, cfg, in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palate: judge fell back to heuristics: %v\n", err)
		storage.BumpDrop(conn, "judge_fallback")
		a, err = HeuristicJudge{}.Assess(ctx, conn, cfg, in)
		if err != nil {
			return nil, a, err
		}
	} else if j.Name() == "llm" {
		storage.BumpHit(conn, "judge_llm")
	}
	// Either source of the rulebook flag (the caller's or the verdict's)
	// is enough to tag the row; the project flag works the same way.
	if in.RulebookOverlap {
		a.RulebookOverlap = true
	}
	if in.ProjectSpecific {
		a.ProjectSpecific = true
	}
	sig := learn.Signal{Type: in.Signal, Statement: a.Statement, Category: a.Category, Evidence: evidence, Weight: in.Confidence, Session: in.Session}
	p, err := learn.ApplyJudged(conn, cfg, sig, a)
	return p, a, err
}

// Correct judges a four-field correction: the fix (or, failing that, what
// was tried) is the statement; the whole record lands on the events log as
// one "correct" event exactly as learn.RecordCorrection does.
func Correct(ctx context.Context, conn *sql.DB, cfg config.Config, c learn.Correction) (*learn.Preference, learn.Assessment, error) {
	stmt := c.Statement()
	p, a, err := Apply(ctx, conn, cfg, Input{Statement: stmt, Signal: learn.SignalExplicitCorrection}, "")
	if err != nil {
		return nil, a, err
	}
	c.Fix = stmt
	payload, _ := json.Marshal(c)
	_ = storage.AppendEvent(conn, "correct", string(payload))
	return p, a, nil
}

// ApplyDistilled is the distill.Judge seam: one distilled statement, judged
// against the compare set and stored through Apply with the distiller's own
// confidence as the hint and its evidence on the event. s.Session and
// s.Project (set by distill.ApplyDistilled before this seam is called) ride
// straight into Input, so the resulting event carries the session key and
// the row is tagged project-specific exactly as the heuristic path does.
func ApplyDistilled(ctx context.Context, conn *sql.DB, cfg config.Config, s distill.DistilledStatement) error {
	_, _, err := Apply(ctx, conn, cfg, Input{Statement: s.Statement, Signal: learn.SignalLLMDistilled, Category: s.Category, Confidence: s.Confidence, RulebookOverlap: s.Rulebook, ProjectSpecific: s.Project, Session: s.Session}, s.Evidence)
	return err
}

// Verdict formats what a write did, for the CLI/MCP reply: the relation, the
// category, and the confidence the row now holds (after reinforcement), not
// the judge's raw candidate score.
func Verdict(p *learn.Preference, a learn.Assessment) string {
	return fmt.Sprintf("%s, %s, c=%.2f, %s", a.Category, a.Relation, p.Confidence, a.By)
}
