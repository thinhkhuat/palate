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
		return "llm (" + cfg.LLM.Model + ")"
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
	sig := learn.Signal{Type: in.Signal, Statement: a.Statement, Category: a.Category, Evidence: evidence}
	p, err := learn.ApplyJudged(conn, cfg, sig, a)
	return p, a, err
}

// Correct judges a four-field correction: the fix (or, failing that, what
// was tried) is the statement; the whole record lands on the events log as
// one "correct" event exactly as learn.RecordCorrection does.
func Correct(ctx context.Context, conn *sql.DB, cfg config.Config, c learn.Correction) (*learn.Preference, error) {
	stmt := c.Statement()
	p, _, err := Apply(ctx, conn, cfg, Input{Statement: stmt, Signal: learn.SignalExplicitCorrection}, "")
	if err != nil {
		return nil, err
	}
	c.Fix = stmt
	payload, _ := json.Marshal(c)
	_ = storage.AppendEvent(conn, "correct", string(payload))
	return p, nil
}
