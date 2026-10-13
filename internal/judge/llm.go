package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
)

// judgeRetries: one corrective round-trip on an unparseable reply, same
// policy as the distiller.
const judgeRetries = 1

// LLMJudge asks the configured gateway for the verdict.
type LLMJudge struct {
	Gateway *distill.Gateway
	Model   string
}

func (*LLMJudge) Name() string { return "llm" }

type verdict struct {
	Statement  string  `json:"statement"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Relation   string  `json:"relation"`
	TargetID   string  `json:"target_id"`
	Reason     string  `json:"reason"`
	Rulebook   bool    `json:"rulebook"`
	// Project is the verdict's own flag that the candidate describes one
	// project's own features, files, components or requirements rather than
	// how the user works (phase-03 req 3) — the same optional field the
	// distiller's response carries.
	Project bool `json:"project"`
}

func (j *LLMJudge) Assess(ctx context.Context, conn *sql.DB, cfg config.Config, in Input) (learn.Assessment, error) {
	// The model compares against the compare set (the profile plus the best
	// full-text matches outside it), not one category: a paraphrase filed
	// elsewhere must still be recognised. The hint only seeds the category.
	hint := in.Category
	if hint == "" {
		hint = learn.InferCategoryFromStatement(in.Statement)
	}
	// The compare set (phase-03 req 2) replaces the whole live ledger: the
	// profile plus a bounded FTS slice keyed on the candidate's own wording.
	// The guard below only ever trusts a target id the model was actually
	// shown, so a smaller set is safe by construction.
	existing, _ := learn.CompareSet(conn, cfg, in.Statement)
	ids := map[string]bool{}
	for _, p := range existing {
		ids[p.ID] = true
	}
	// The curated rulebook (when configured) rides along so the verdict can
	// flag restatements; empty = the feature is off and the section is
	// omitted.
	rulebookBlock, _ := learn.LoadRulebook(cfg)
	var v verdict
	validate := func(raw string) error {
		var tmp verdict
		if err := json.Unmarshal([]byte(stripFence(raw)), &tmp); err != nil {
			return fmt.Errorf("verdict is not a JSON object: %w", err)
		}
		if strings.TrimSpace(tmp.Statement) == "" {
			return fmt.Errorf("verdict has an empty statement")
		}
		switch tmp.Relation {
		case "new", "same", "refines", "contradicts":
		default:
			return fmt.Errorf("verdict relation %q is not one of new|same|refines|contradicts", tmp.Relation)
		}
		v = tmp
		return nil
	}
	if _, err := j.Gateway.Complete(ctx, SystemPrompt, BuildUserPrompt(in, existing, rulebookBlock), judgeRetries, validate); err != nil {
		return learn.Assessment{}, err
	}
	a := learn.Assessment{
		Statement:       strings.TrimSpace(v.Statement),
		Category:        strings.TrimSpace(v.Category),
		Confidence:      learn.ClampConfidence(v.Confidence, cfg.CFloor),
		Relation:        learn.Relation(v.Relation),
		TargetID:        v.TargetID,
		Reason:          v.Reason,
		By:              "llm",
		RulebookOverlap: v.Rulebook,
		ProjectSpecific: v.Project,
	}
	if a.Category == "" {
		a.Category = hint
	}
	// A target the model did not receive cannot be trusted; degrade to new.
	if a.Relation != learn.RelationNew && !ids[a.TargetID] {
		a.Relation, a.TargetID = learn.RelationNew, ""
	}
	return a, nil
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
