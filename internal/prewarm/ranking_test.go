package prewarm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
)

// TestSessionPrewarmRanksByConfidence: gate G5 — over budget, the block keeps
// the highest-confidence rows and drops the weakest first. Before the fix,
// fitLines kept file-leading lines (Communication/General verbatim garbage)
// and truncated real Workflow/Tooling signal off the end.
func TestSessionPrewarmRanksByConfidence(t *testing.T) {
	db, cfg := openMem(t)
	// Weak rows first in category order (General < Workflow alphabetically, and
	// inserted first), strong rows last — file order favours the junk. 12 weak
	// vs 4 strong with a budget that fits the 4 strong but not all 12 weak:
	// file-order greedy keeps ~6 weak and truncates strong; confidence-ranked
	// keeps all 4 strong and drops weak first.
	for i := 0; i < 12; i++ {
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: fmt.Sprintf("weak junk habit %d", i), Category: "General"}, nil)
		_, _ = db.Exec(`UPDATE preferences SET confidence=0.30 WHERE statement LIKE 'weak junk habit `+fmt.Sprintf("%d", i)+`%'`)
	}
	for i := 0; i < 4; i++ {
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: fmt.Sprintf("strong real habit %d", i), Category: "Workflow"}, nil)
		_, _ = db.Exec(`UPDATE preferences SET confidence=0.90 WHERE statement LIKE 'strong real habit `+fmt.Sprintf("%d", i)+`%'`)
	}
	cfg.TokenBudgets.PrewarmSession = 130
	block, err := SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.Content, "more preferences not shown") {
		t.Fatalf("over budget, the block must say what it dropped:\n%s", block.Content)
	}
	for i := 0; i < 4; i++ {
		if !strings.Contains(block.Content, fmt.Sprintf("strong real habit %d", i)) {
			t.Fatalf("strong row %d truncated while weak rows kept:\n%s", i, block.Content)
		}
	}
	weakKept := strings.Count(block.Content, "weak junk habit")
	if weakKept > 6 {
		t.Fatalf("only %d of 12 weak rows dropped; weakest must drop first:\n%s", 12-weakKept, block.Content)
	}
}
