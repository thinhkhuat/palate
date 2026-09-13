package judge

import (
	"context"
	"database/sql"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
)

// HeuristicJudge is the pre-0.3 behaviour, verbatim: category from the
// keyword switch, confidence weight×0.5, exact-statement match → same,
// negation/antonym contradiction → contradicts, near-duplicate (only for
// distilled signals, as before) → same, otherwise new.
type HeuristicJudge struct{}

func (HeuristicJudge) Name() string { return "heuristic" }

func (HeuristicJudge) Assess(_ context.Context, conn *sql.DB, cfg config.Config, in Input) (learn.Assessment, error) {
	cat := in.Category
	if cat == "" {
		cat = learn.InferCategoryFromStatement(in.Statement)
	}
	conf := learn.InitialConfidence(cfg.WeightFor(string(in.Signal)), cfg.CFloor)
	if in.Signal == learn.SignalImported {
		// Reimport always landed at the imported weight itself (0.4), not
		// weight×0.5 — keep that quarantine level byte-for-byte.
		conf = cfg.Weights.Imported
	}
	a := learn.Assessment{
		Statement:  in.Statement,
		Category:   cat,
		Confidence: conf,
		Relation:   learn.RelationNew,
		By:         "heuristic",
	}
	if p, err := learn.FindByStatement(conn, cat, in.Statement); err == nil && p != nil {
		a.Relation, a.TargetID = learn.RelationSame, p.ID
		return a, nil
	}
	if p, err := learn.FindContradiction(conn, cat, in.Statement); err == nil && p != nil {
		a.Relation, a.TargetID = learn.RelationContradicts, p.ID
		return a, nil
	}
	if in.Signal == learn.SignalLLMDistilled {
		if p, err := learn.FindNearDuplicate(conn, cat, in.Statement); err == nil && p != nil {
			a.Relation, a.TargetID = learn.RelationSame, p.ID
		}
	}
	return a, nil
}
