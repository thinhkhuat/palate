package learn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

func TestLoadRulebook(t *testing.T) {
	if _, ok := LoadRulebook(config.Config{}); ok {
		t.Fatal("no rulebook configured: the feature must be off")
	}
	dir := t.TempDir()
	if _, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "absent.md")}); ok {
		t.Fatal("an unreadable rulebook must fail open")
	}
	path := filepath.Join(dir, "CLAUDE.md")
	content := "Always use table-driven tests.\n\nNever hardcode tokens like ghp_xxxxxxxxxxxxxxxxxxxx.\n\nKeep it simple.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	block, ok := LoadRulebook(config.Config{Rulebook: path})
	if !ok {
		t.Fatal("a readable rulebook must load")
	}
	if strings.Contains(block, "ghp_") {
		t.Fatal("a secret-shaped line must be redacted before it can reach a gateway")
	}
	if !strings.Contains(block, "[redacted secret]") || !strings.Contains(block, "Keep it simple.") {
		t.Fatalf("unexpected block: %q", block)
	}
	if strings.Count(block, "\n") != 2 {
		t.Fatalf("blank lines carry no rule and must be dropped: %q", block)
	}
}

// TestLoadRulebookImports covers phase-03 req 5: @import lines, recursion
// depth, a cycle guard, a missing import that does not fail the whole
// rulebook, the old 8 KB per-file cap being gone, the new 256 KB ceiling,
// and secret redaction reaching into an imported file too.
func TestLoadRulebookImports(t *testing.T) {
	t.Run("relative @import is included", func(t *testing.T) {
		dir := t.TempDir()
		writeRule(t, dir, "RULES.md", "Never hardcode secrets.\nAlways lint before commit.")
		writeRule(t, dir, "CLAUDE.md", "@RULES.md\nPrefer small functions.")
		block, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "CLAUDE.md")})
		if !ok {
			t.Fatal("expected the rulebook to load")
		}
		for _, want := range []string{"Never hardcode secrets.", "Always lint before commit.", "Prefer small functions."} {
			if !strings.Contains(block, want) {
				t.Fatalf("missing %q in block: %q", want, block)
			}
		}
	})

	t.Run("nested import at depth 2", func(t *testing.T) {
		dir := t.TempDir()
		writeRule(t, dir, "deep.md", "Deepest rule.")
		writeRule(t, dir, "mid.md", "@deep.md\nMiddle rule.")
		writeRule(t, dir, "root.md", "@mid.md\nRoot rule.")
		block, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "root.md")})
		if !ok {
			t.Fatal("expected the rulebook to load")
		}
		for _, want := range []string{"Deepest rule.", "Middle rule.", "Root rule."} {
			if !strings.Contains(block, want) {
				t.Fatalf("missing %q in block: %q", want, block)
			}
		}
	})

	t.Run("cycle A to B to A terminates", func(t *testing.T) {
		dir := t.TempDir()
		writeRule(t, dir, "a.md", "@b.md\nRule A.")
		writeRule(t, dir, "b.md", "@a.md\nRule B.")
		done := make(chan struct{})
		var block string
		var ok bool
		go func() {
			block, ok = LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "a.md")})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a cycle must terminate, not hang")
		}
		if !ok || !strings.Contains(block, "Rule A.") || !strings.Contains(block, "Rule B.") {
			t.Fatalf("cycle must still yield both rules exactly once: ok=%v block=%q", ok, block)
		}
		if strings.Count(block, "Rule A.") != 1 {
			t.Fatalf("a cycle must not duplicate content: %q", block)
		}
	})

	t.Run("missing import is skipped, not fatal", func(t *testing.T) {
		dir := t.TempDir()
		writeRule(t, dir, "root.md", "@absent.md\nStill here.")
		block, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "root.md")})
		if !ok {
			t.Fatal("a missing import must not fail the whole rulebook")
		}
		if !strings.Contains(block, "Still here.") {
			t.Fatalf("the rest of the file must still load: %q", block)
		}
	})

	t.Run("more than 8 KB total is no longer cut", func(t *testing.T) {
		dir := t.TempDir()
		var big strings.Builder
		for i := 0; i < 400; i++ {
			big.WriteString("rule line that keeps going and going\n")
		}
		writeRule(t, dir, "root.md", big.String())
		block, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "root.md")})
		if !ok {
			t.Fatal("expected the rulebook to load")
		}
		// 400 * ~39 bytes ≈ 15.6 KB, comfortably over the old 8 KB cap and
		// still under the new 256 KB ceiling.
		if len(block) < 8<<10 {
			t.Fatalf("expected the block to exceed the old 8 KB cap, got %d bytes", len(block))
		}
	})

	t.Run("more than 256 KB is cut with a warning", func(t *testing.T) {
		dir := t.TempDir()
		var huge strings.Builder
		for i := 0; i < 8000; i++ {
			huge.WriteString("rule line that keeps going and going\n")
		}
		writeRule(t, dir, "root.md", huge.String())
		stderr := captureStderr(t, func() {
			capped, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "root.md")})
			if !ok || len(capped) > rulebookCeilingBytes {
				t.Fatalf("ceiling not applied: ok=%v len=%d", ok, len(capped))
			}
			if !strings.HasSuffix(capped, "going") {
				t.Fatalf("the ceiling must land on a line boundary, not split a rule: %q", capped[len(capped)-40:])
			}
		})
		if !strings.Contains(stderr, "exceeds") {
			t.Fatalf("exceeding the ceiling must warn on stderr, got %q", stderr)
		}
	})

	t.Run("secret line is redacted inside an imported file", func(t *testing.T) {
		dir := t.TempDir()
		writeRule(t, dir, "imported.md", "Never hardcode tokens like ghp_xxxxxxxxxxxxxxxxxxxx.\nClean rule.")
		writeRule(t, dir, "root.md", "@imported.md")
		block, ok := LoadRulebook(config.Config{Rulebook: filepath.Join(dir, "root.md")})
		if !ok {
			t.Fatal("expected the rulebook to load")
		}
		if strings.Contains(block, "ghp_") {
			t.Fatalf("a secret-shaped line inside an import must be redacted: %q", block)
		}
		if !strings.Contains(block, "[redacted secret]") || !strings.Contains(block, "Clean rule.") {
			t.Fatalf("unexpected block: %q", block)
		}
	})
}

func writeRule(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplyJudgedRulebookTag(t *testing.T) {
	db, cfg := testDB(t)
	a := Assessment{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.7, Relation: RelationNew, By: "test", RulebookOverlap: true}
	p, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: a.Statement}, a)
	if err != nil {
		t.Fatal(err)
	}
	got := preferenceByID(db, p.ID)
	if got == nil {
		t.Fatal("row not stored")
	}
	if !HasRulebookOverlap(got.Provenance) {
		t.Fatalf("a rulebook-overlap write must carry the tag in provenance: %q", got.Provenance)
	}
}

func TestApplyJudgedRulebookTagPropagatesToTarget(t *testing.T) {
	db, cfg := testDB(t)
	orig := Assessment{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.6, Relation: RelationNew, By: "test"}
	p, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: orig.Statement}, orig)
	if err != nil {
		t.Fatal(err)
	}
	if HasRulebookOverlap(preferenceByID(db, p.ID).Provenance) {
		t.Fatal("untagged write must not carry the tag")
	}
	reinforce := Assessment{Statement: p.Statement, Category: "Testing", Confidence: 0.7, Relation: RelationSame, TargetID: p.ID, By: "test", RulebookOverlap: true}
	if _, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: reinforce.Statement}, reinforce); err != nil {
		t.Fatal(err)
	}
	got := preferenceByID(db, p.ID)
	if got == nil || !HasRulebookOverlap(got.Provenance) {
		t.Fatalf("reinforcing with rulebook evidence must tag the target: %q", got.Provenance)
	}
}

// TestLoadRulebookLive proves requirement 5 against the owner's real,
// always-loaded rulebook (~/.claude/CLAUDE.md, importing @RULES.md) — read
// only, never written. Skipped unless PALATE_TEST_LIVE_RULEBOOK=1, since a
// CI box or a fresh clone has no such file.
func TestLoadRulebookLive(t *testing.T) {
	if os.Getenv("PALATE_TEST_LIVE_RULEBOOK") != "1" {
		t.Skip("set PALATE_TEST_LIVE_RULEBOOK=1 to run against the owner's real rulebook")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	block, ok := LoadRulebook(config.Config{Rulebook: path})
	if !ok {
		t.Fatalf("expected %s to load", path)
	}
	t.Logf("bytes=%d", len(block))
	if len(block) < 30000 {
		t.Fatalf("expected the imported RULES.md to bring the total to >= 30000 bytes, got %d", len(block))
	}
}
