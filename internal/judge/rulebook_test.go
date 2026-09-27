package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
)

func TestLLMVerdictRulebookFlag(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, rulebookVerdict("Prefers table-driven tests.", true))
	cfg := llmCfg(t, srv)
	gw, ok := distill.NewGatewayFromEnv(cfg)
	if !ok {
		t.Fatal("gateway must build from the configured env")
	}
	j := &LLMJudge{Gateway: gw}
	a, err := j.Assess(context.Background(), db, cfg, Input{Statement: "Prefers table-driven tests."})
	if err != nil {
		t.Fatal(err)
	}
	if !a.RulebookOverlap {
		t.Fatal("a verdict with rulebook:true must set Assessment.RulebookOverlap")
	}
}

// TestApplyRulebookCallerFlag: the distiller's own flag is enough — the row
// must be tagged even when the verdict does not repeat it.
func TestApplyRulebookCallerFlag(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, rulebookVerdict("Prefers table-driven tests.", false))
	cfg := llmCfg(t, srv)
	p, _, err := Apply(context.Background(), db, cfg, Input{
		Statement:       "Prefers table-driven tests.",
		Signal:          learn.SignalExplicitPrefer,
		RulebookOverlap: true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var prov string
	if err := db.QueryRow(`SELECT provenance FROM preferences WHERE id=?`, p.ID).Scan(&prov); err != nil {
		t.Fatal(err)
	}
	if !learn.HasRulebookOverlap(prov) {
		t.Fatalf("caller flag must tag the row: %q", prov)
	}
}

func TestBuildUserPromptRulebookSection(t *testing.T) {
	out := BuildUserPrompt(Input{Statement: "Prefers tabs."}, nil, "Always lint before commit.")
	if !strings.Contains(out, "<rulebook>\nAlways lint before commit.\n</rulebook>") {
		t.Fatalf("a configured rulebook must appear as a <rulebook> section: %q", out)
	}
	out = BuildUserPrompt(Input{Statement: "Prefers tabs."}, nil, "")
	if strings.Contains(out, "<rulebook>") {
		t.Fatal("no configured rulebook: the section must be omitted so the model never guesses")
	}
}

func rulebookVerdict(stmt string, rulebook bool) string {
	b, _ := json.Marshal(map[string]interface{}{
		"statement": stmt, "category": "Testing", "confidence": 0.7,
		"relation": "new", "target_id": "", "rulebook": rulebook, "reason": "test",
	})
	return string(b)
}

// TestLLMVerdictProjectFlag mirrors TestLLMVerdictRulebookFlag for the
// "project" verdict field (phase-03 req 3).
func TestLLMVerdictProjectFlag(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, projectVerdict("Uses a Makefile target named build-linux.", true))
	cfg := llmCfg(t, srv)
	gw, ok := distill.NewGatewayFromEnv(cfg)
	if !ok {
		t.Fatal("gateway must build from the configured env")
	}
	j := &LLMJudge{Gateway: gw}
	a, err := j.Assess(context.Background(), db, cfg, Input{Statement: "Uses a Makefile target named build-linux."})
	if err != nil {
		t.Fatal(err)
	}
	if !a.ProjectSpecific {
		t.Fatal("a verdict with project:true must set Assessment.ProjectSpecific")
	}
}

// TestApplyProjectCallerFlag mirrors TestApplyRulebookCallerFlag: the
// caller's own flag is enough to tag the row even when the verdict omits it.
func TestApplyProjectCallerFlag(t *testing.T) {
	db := testDB(t)
	srv, _ := fakeGateway(t, projectVerdict("Uses a Makefile target named build-linux.", false))
	cfg := llmCfg(t, srv)
	p, _, err := Apply(context.Background(), db, cfg, Input{
		Statement:       "Uses a Makefile target named build-linux.",
		Signal:          learn.SignalExplicitPrefer,
		ProjectSpecific: true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var prov string
	if err := db.QueryRow(`SELECT provenance FROM preferences WHERE id=?`, p.ID).Scan(&prov); err != nil {
		t.Fatal(err)
	}
	if !learn.HasProjectSpecific(prov) {
		t.Fatalf("caller flag must tag the row: %q", prov)
	}
}

func TestSystemPromptMentionsProject(t *testing.T) {
	if !strings.Contains(SystemPrompt, `"project"`) {
		t.Fatalf("SystemPrompt must instruct the model on the project flag: %s", SystemPrompt)
	}
}

func projectVerdict(stmt string, project bool) string {
	b, _ := json.Marshal(map[string]interface{}{
		"statement": stmt, "category": "Tooling", "confidence": 0.7,
		"relation": "new", "target_id": "", "project": project, "reason": "test",
	})
	return string(b)
}
