package prewarm

import (
	"database/sql"
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

func TestSessionPrewarmBudget(t *testing.T) {
	db, cfg := openMem(t)
	for i := 0; i < 30; i++ {
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "pref number test", Category: "General"}, nil)
	}
	block, err := SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if block.Tokens > cfg.TokenBudgets.PrewarmSession+10 {
		t.Fatalf("session prewarm over budget: %d > %d", block.Tokens, cfg.TokenBudgets.PrewarmSession)
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
	if !strings.Contains(distilledLine, ", distilled)") {
		t.Fatalf("distilled row missing marker: %q", distilledLine)
	}
	statedLine := lineContaining(block.Content, "use early returns over nested ifs")
	if strings.Contains(statedLine, "distilled") {
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
