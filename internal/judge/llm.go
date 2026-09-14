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
}

func (j *LLMJudge) Assess(ctx context.Context, conn *sql.DB, cfg config.Config, in Input) (learn.Assessment, error) {
	// The model compares against the whole active profile, not one category:
	// a paraphrase filed elsewhere must still be recognised (Command Code's
	// learner reads every taste file). The hint only seeds the category.
	hint := in.Category
	if hint == "" {
		hint = learn.InferCategoryFromStatement(in.Statement)
	}
	existing, _ := learn.ListPreferences(conn, false)
	ids := map[string]bool{}
	for _, p := range existing {
		ids[p.ID] = true
	}
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
	if _, err := j.Gateway.Complete(ctx, SystemPrompt, BuildUserPrompt(in, existing), judgeRetries, validate); err != nil {
		return learn.Assessment{}, err
	}
	a := learn.Assessment{
		Statement:  strings.TrimSpace(v.Statement),
		Category:   strings.TrimSpace(v.Category),
		Confidence: learn.ClampConfidence(v.Confidence, cfg.CFloor),
		Relation:   learn.Relation(v.Relation),
		TargetID:   v.TargetID,
		Reason:     v.Reason,
		By:         "llm",
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
