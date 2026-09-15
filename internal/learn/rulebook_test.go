package learn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	var big strings.Builder
	for i := 0; i < 1000; i++ {
		big.WriteString("rule line that keeps going and going\n")
	}
	if err := os.WriteFile(path, []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	capped, ok := LoadRulebook(config.Config{Rulebook: path})
	if !ok || len(capped) > rulebookMaxBytes {
		t.Fatalf("cap not applied: ok=%v len=%d", ok, len(capped))
	}
	if !strings.HasSuffix(capped, "going") {
		t.Fatalf("the cap must land on a line boundary, not split a rule: %q", capped[len(capped)-40:])
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
