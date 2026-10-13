package prewarm

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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
	for _, h := range []string{"## Testing", "## Workflow", "25 preferences, the whole profile"} {
		if !strings.Contains(block.Content, h) {
			t.Fatalf("block missing %q:\n%s", h, block.Content)
		}
	}
	if strings.Contains(block.Content, "more preferences not shown") {
		t.Fatalf("under budget, nothing should be dropped:\n%s", block.Content)
	}
}

// Over budget, SelectProfile (phase-01) decides which rows fit — skip and
// continue, never a partial line (the 0.3.x cut landed mid-sentence).
// SessionPrewarm has no fitting of its own and no "N shown" variant (req 2):
// it carries exactly what Body returns, always within budget.
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
	if block.Tokens > cfg.TokenBudgets.PrewarmSession {
		t.Fatalf("session prewarm over budget: %d > %d", block.Tokens, cfg.TokenBudgets.PrewarmSession)
	}
	shown := strings.Count(block.Content, "budget statement number")
	if shown == 0 || shown >= 40 {
		t.Fatalf("expected the budget to drop some of the 40 rows and keep the rest, got %d shown:\n%s", shown, block.Content)
	}
	if strings.Contains(block.Content, "more preferences not shown") {
		t.Fatalf("the old fitting message is retired — selection drops rows, prewarm never announces it:\n%s", block.Content)
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

// An empty profile still tells the agent the feature exists and fills itself
// in (Command Code's "No preferences learned yet … learned automatically").
func TestSessionPrewarmEmptyProfile(t *testing.T) {
	db, cfg := openMem(t)
	block, err := SessionPrewarm(db, cfg)
	if err != nil || !strings.Contains(block.Content, "No preferences learned yet") || !strings.Contains(block.Content, "automatically") || strings.Contains(block.Content, "## ") {
		t.Fatalf("empty profile must explain itself without headings, got %+v err=%v", block, err)
	}
}

func TestGoalDedup(t *testing.T) {
	db, cfg := openMem(t)
	cfg.TokenBudgets.PrewarmGoal = 200 // the goal block defaults off (req 9); this test targets its dedup, not the gate
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
	cfg.TokenBudgets.PrewarmGoal = 200 // the goal block defaults off (req 9); this test targets deduper isolation, not the gate
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

// TestPrewarmMarksDistilled: a distilled row needs to be seen again in a
// second distinct session before it enters the profile (phase-01
// eligibility) — one session leaves it a candidate, invisible to
// SessionPrewarm; a second session's reinforcement admits it.
func TestPrewarmMarksDistilled(t *testing.T) {
	db, cfg := openMem(t)
	sig := learn.Signal{
		Type:      learn.SignalLLMDistilled,
		Statement: "Prefers table-driven tests.",
		Category:  "Testing",
		Weight:    0.9,
		Session:   "s1",
	}
	if _, err := learn.ApplySignal(db, cfg, sig, nil); err != nil {
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
	if strings.Contains(block.Content, "Prefers table-driven tests.") {
		t.Fatalf("a distilled row seen once must stay a candidate: %q", block.Content)
	}

	sig.Session = "s2"
	if _, err := learn.ApplySignal(db, cfg, sig, nil); err != nil {
		t.Fatal(err)
	}
	block, err = SessionPrewarm(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.Content, "Prefers table-driven tests.") {
		t.Fatalf("prewarm content missing the now-eligible distilled statement: %q", block.Content)
	}
	for _, l := range strings.Split(block.Content, "\n") {
		if strings.HasPrefix(l, "- ") && strings.Contains(l, "(") && strings.Contains(l, "Confidence:") {
			t.Fatalf("internal metadata suffix leaked into prewarm: %q", l)
		}
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
	goalCfg := config.Default()
	goalCfg.TokenBudgets.PrewarmGoal = 200 // the goal block defaults off (req 9); this test targets its conflict filter
	g, fired, _ := GoalPrewarm(db, goalCfg, "write table-driven tests for the parser")
	if !fired {
		t.Fatal("goal prewarm should fire with prewarm_goal > 0 and a matching keyword")
	}
	if strings.Contains(g.Content, "never table-driven") {
		t.Fatalf("goal prewarm must hide the conflict loser:\n%s", g.Content)
	}
}

// TestGoalBlockOffByDefault: with the default config (prewarm_goal: 0), the
// user-prompt hook's goal block never fires, whatever the prompt (req 9).
func TestGoalBlockOffByDefault(t *testing.T) {
	db, cfg := openMem(t)
	_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "use table-driven tests for testing", Category: "Testing"}, nil)
	if cfg.TokenBudgets.PrewarmGoal != 0 {
		t.Fatalf("default prewarm_goal must be 0 (off), got %d", cfg.TokenBudgets.PrewarmGoal)
	}
	block, fired, err := GoalPrewarm(db, cfg, "I need to write table-driven tests for my Go code")
	if err != nil {
		t.Fatal(err)
	}
	if fired || block.Content != "" {
		t.Fatalf("the goal block must not fire by default: fired=%v content=%q", fired, block.Content)
	}
}

// TestGoalTruncateWholeLines: over a tiny budget with multi-byte text, the
// goal block still ends in valid UTF-8, keeps only whole lines, and the
// closing tag survives — no raw byte-offset cut (req 9).
func TestGoalTruncateWholeLines(t *testing.T) {
	db, cfg := openMem(t)
	cfg.TokenBudgets.PrewarmGoal = 30
	for i := 0; i < 5; i++ {
		_, _ = learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: fmt.Sprintf("Ưu tiên kiểm thử theo kiểu bảng cho trường hợp số %d", i), Category: "Testing"}, nil)
	}
	block, fired, err := GoalPrewarm(db, cfg, "kiểm thử theo kiểu bảng cho việc này")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("expected the goal block to fire with prewarm_goal > 0 and a matching keyword")
	}
	if !utf8.ValidString(block.Content) {
		t.Fatalf("goal block must stay valid UTF-8, got %q", block.Content)
	}
	if !strings.HasSuffix(block.Content, "</palate-prewarm>") {
		t.Fatalf("goal block must keep its closing tag, got %q", block.Content)
	}
	for _, l := range strings.Split(block.Content, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "<palate-prewarm") || strings.HasPrefix(trimmed, "</palate-prewarm") {
			continue
		}
		if !strings.HasPrefix(trimmed, "- [") {
			t.Fatalf("expected only whole bullet lines, got: %q", l)
		}
	}
}
