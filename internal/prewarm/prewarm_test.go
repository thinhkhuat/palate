package prewarm

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func openMem(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return db, config.Default()
}

// Under budget the block carries every active preference, grouped under its
// "## Category" heading, in the same line shape as TASTE.md.
func TestSessionPrewarmCarriesWholeProfile(t *testing.T) {
	db, cfg := openMem(t)
	for i := 0; i < 25; i++ {
		cat := "Testing"
		if i%2 == 1 {
			cat = "Workflow"
		}
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: fmt.Sprintf("whole profile statement number %d", i), Category: cat}, nil)
	}
	block, err := SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if !strings.Contains(block.Content, fmt.Sprintf("whole profile statement number %d.", i)) {
			t.Fatalf("statement %d missing from the block:\n%s", i, block.Content)
		}
	}
	for _, h := range []string{"## Testing", "## Workflow", "all 25 preferences"} {
		if !strings.Contains(block.Content, h) {
			t.Fatalf("block missing %q:\n%s", h, block.Content)
		}
	}
	if strings.Contains(block.Content, "more preferences not shown") {
		t.Fatalf("under budget, nothing should be dropped:\n%s", block.Content)
	}
}

// Over budget the block drops whole bullets only and says how many, never a
// partial line (the 0.3.x cut landed mid-sentence).
func TestSessionPrewarmBudgetDropsWholeLines(t *testing.T) {
	db, cfg := openMem(t)
	for i := 0; i < 40; i++ {
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: fmt.Sprintf("budget statement number %d with some padding words to cost tokens", i), Category: "General"}, nil)
	}
	cfg.TokenBudgets.PrewarmSession = 300
	block, err := SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if block.Tokens > cfg.TokenBudgets.PrewarmSession+10 {
		t.Fatalf("session prewarm over budget: %d > %d", block.Tokens, cfg.TokenBudgets.PrewarmSession)
	}
	if !strings.Contains(block.Content, "more preferences not shown") {
		t.Fatalf("over budget, the block must say what it dropped:\n%s", block.Content)
	}
	if strings.Contains(block.Content, "all 40 preferences") || !strings.Contains(block.Content, "40 preferences, ") {
		t.Fatalf("a truncated block must not claim \"all\":\n%s", block.Content)
	}
	for _, l := range strings.Split(block.Content, "\n") {
		if strings.HasPrefix(l, "- ") && !strings.Contains(l, "Confidence: ") {
			t.Fatalf("partial bullet leaked: %q", l)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(block.Content), "</palate-prewarm>") {
		t.Fatalf("block not closed:\n%s", block.Content)
	}
}

func TestSessionPrewarmEmptyProfile(t *testing.T) {
	db, cfg := openMem(t)
	block, err := SessionPrewarm(db, cfg)
	if err != nil || block.Content != "" || block.Tokens != 0 {
		t.Fatalf("empty profile must inject nothing, got %+v err=%v", block, err)
	}
}

func TestGoalDedup(t *testing.T) {
	db, cfg := openMem(t)
	_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "use table-driven tests for testing", Category: "Testing"}, nil)
	ResetGoalDedup()
	_, fired1, _ := GoalPrewarm(db, cfg, "I need to write table-driven tests for my Go code")
	if !fired1 {
		t.Fatalf("first goal should fire")
	}
	_, fired2, _ := GoalPrewarm(db, cfg, "I need to write table-driven tests for my Go code")
	if fired2 {
		t.Fatalf("same goal should not fire twice")
	}
	_, fired3, _ := GoalPrewarm(db, cfg, "now about error handling wraps")
	_ = fired3
}

func TestDeduperIsolated(t *testing.T) {
	db, cfg := openMem(t)
	_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "use table-driven tests for testing", Category: "Testing"}, nil)
	prompt := "I need to write table-driven tests for my Go code"
	a, b := &Deduper{}, &Deduper{}
	if _, fired, _ := a.GoalPrewarm(db, cfg, prompt); !fired {
		t.Fatalf("first deduper should fire")
	}
	if _, fired, _ := b.GoalPrewarm(db, cfg, prompt); !fired {
		t.Fatalf("separate deduper must fire independently (no shared global)")
	}
	if _, fired, _ := a.GoalPrewarm(db, cfg, prompt); fired {
		t.Fatalf("same deduper should suppress repeats")
	}
}

func TestPrewarmMarksDistilled(t *testing.T) {
	db, cfg := openMem(t)
	if _, err := learn.ApplySignal(db, cfg, learn.Signal{
		Type:      learn.SignalLLMDistilled,
		Statement: "Prefers table-driven tests.",
		Category:  "Testing",
		Weight:    0.9,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := learn.ApplySignal(db, cfg, learn.Signal{
		Type:      learn.SignalExplicitPrefer,
		Statement: "use early returns over nested ifs",
		Category:  "Error Handling",
	}, nil); err != nil {
		t.Fatal(err)
	}

	block, err := SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.Content, "Prefers table-driven tests.") {
		t.Fatalf("prewarm content missing the distilled statement: %q", block.Content)
	}
	distilledLine := lineContaining(block.Content, "Prefers table-driven tests.")
	if !strings.Contains(distilledLine, ", DISTILLED)") {
		t.Fatalf("distilled row missing tier: %q", distilledLine)
	}
	statedLine := lineContaining(block.Content, "use early returns over nested ifs")
	if strings.Contains(statedLine, "DISTILLED") {
		t.Fatalf("STATED row must not carry the distilled marker: %q", statedLine)
	}
}

func lineContaining(content, substr string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// TestPrewarmHidesConflictLosers: a contradicted row (the conflict's
// conflicting_id) must not be injected next to the row it contradicts.
func TestPrewarmHidesConflictLosers(t *testing.T) {
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storage.Close)
	_, _ = db.Exec(`INSERT INTO preferences(id, category, statement, confidence) VALUES('w','Testing','Prefers table-driven tests.',0.9)`)
	_, _ = db.Exec(`INSERT INTO preferences(id, category, statement, confidence) VALUES('l','Testing','Prefers one assertion per test, never table-driven.',0.4)`)
	_, _ = db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES('c','w','l','contradiction')`)
	b, err := SessionPrewarm(db, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Content, "table-driven tests.") || strings.Contains(b.Content, "never table-driven") {
		t.Fatalf("session prewarm must hide the conflict loser:\n%s", b.Content)
	}
	g, fired, _ := GoalPrewarm(db, config.Default(), "write table-driven tests for the parser")
	if fired && strings.Contains(g.Content, "never table-driven") {
		t.Fatalf("goal prewarm must hide the conflict loser:\n%s", g.Content)
	}
}
