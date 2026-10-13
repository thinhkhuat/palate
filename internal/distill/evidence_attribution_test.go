package distill

import "testing"

// TestEvidenceMustCiteTheUser uses evidence shaped the way the live learner
// writes it (quotes joined by " / ", elided with "...", annotated with
// "(in reply to ...)") from a session where the assistant, not the user,
// described the behaviour. A statement stays only when one quoted piece of
// its evidence is the user's own typed text.
func TestEvidenceMustCiteTheUser(t *testing.T) {
	batch := []Message{
		{Role: "user", Content: "1. yes"},
		{Role: "user", Content: "jev via jevd - please"},
		{Role: "assistant", Content: "Under AFK I decide its three open questions (taking its safer picks), logged in the decisions file."},
		{Role: "assistant", Content: "Phase 6: rebuild the profile on a copy of your store. Your live store is not touched at any step."},
		{Role: "assistant", Content: "Confirmed: palate off on purpose — noted."},
		{Role: "assistant", Content: "Stale docs/security.md: fix them now. Rule [75] says no stale text anywhere."},
	}
	cases := []struct {
		name, evidence string
		keep           bool
	}{
		{"assistant quotes joined by slash", `"Under AFK I decide its three open questions" / "Rule [75] says no stale text anywhere."`, false},
		{"assistant text elided", "Phase 6: rebuild the profile on a copy of your store ... Your live store is not touched at any step.", false},
		{"paraphrase found nowhere", "the user seems to like safe defaults", false},
		{"bare short word that is not a whole user message", `"yes"`, false},
		{"user quote with speaker prefix", `User: "jev via jevd - please"`, true},
		{"assistant acknowledgement written after the user's reply", `"Confirmed: palate off on purpose — noted." (in reply to the user's "1. yes")`, false},
	}
	var statements []DistilledStatement
	for _, c := range cases {
		statements = append(statements, DistilledStatement{Statement: "Prefers " + c.name + ".", Category: "General", Confidence: 0.6, Evidence: c.evidence})
	}
	kept, dropped := filterAssistantOnlyEvidence(testDB(t), nil, batch, statements)
	got := map[string]bool{}
	for _, s := range kept {
		got[s.Statement] = true
	}
	want := 0
	for _, c := range cases {
		if got["Prefers "+c.name+"."] != c.keep {
			t.Errorf("%s: kept=%v, want %v (evidence %q)", c.name, !c.keep, c.keep, c.evidence)
		}
		if !c.keep {
			want++
		}
	}
	if dropped != want {
		t.Errorf("dropped = %d, want %d", dropped, want)
	}
}

// TestApprovedProposalCountsOnlyWhenTheUserRepliedToIt: assistant text is
// evidence only when the evidence also quotes a line of the user's very next
// message (an approval such as "1. yes" inside a longer reply). The same
// proposal paired with a "yes" from a later, unrelated reply is dropped, and
// a proposal the user never answered stays out.
func TestApprovedProposalCountsOnlyWhenTheUserRepliedToIt(t *testing.T) {
	batch := []Message{
		{Role: "assistant", Content: "Did you turn palate off in Claude Code on purpose? I recommend confirming before any palate work starts again."},
		{Role: "user", Content: "1. yes\n2. go ahead - i have subs with CMD"},
		{Role: "assistant", Content: "I'll fix the round-4 findings before phase 6 runs on the copy of your store."},
		{Role: "assistant", Content: "Under AFK I decide its three open questions, taking its safer picks."},
		{Role: "user", Content: "sure"},
	}
	cases := []struct {
		name, evidence string
		keep           bool
	}{
		{"approved proposal, one line of a multi-line reply", `"Did you turn palate off in Claude Code on purpose" / "1. yes"`, true},
		{"approval taken from a later unrelated reply", `"Did you turn palate off in Claude Code on purpose" / "sure"`, false},
		{"proposal that is not the last assistant message before the reply", `"fix the round-4 findings before phase 6" (user: "yes")`, false},
		{"assistant text with no user line at all", `"Under AFK I decide its three open questions"`, false},
	}
	var statements []DistilledStatement
	for _, c := range cases {
		statements = append(statements, DistilledStatement{Statement: "Prefers " + c.name + ".", Category: "General", Confidence: 0.6, Evidence: c.evidence})
	}
	kept, _ := filterAssistantOnlyEvidence(testDB(t), nil, batch, statements)
	got := map[string]bool{}
	for _, s := range kept {
		got[s.Statement] = true
	}
	for _, c := range cases {
		if got["Prefers "+c.name+"."] != c.keep {
			t.Errorf("%s: kept=%v, want %v (evidence %q)", c.name, !c.keep, c.keep, c.evidence)
		}
	}
}

// TestGuardReviewFindings pins the independent review's findings: user text
// elsewhere in the batch never vouches for assistant text (C1); only a short,
// approval-shaped reply to the last assistant message counts (C2); a short
// whole user message never grounds a paraphrase (H1); and quote variants a
// model produces from the user's own words still ground (curly single quotes,
// guillemets, a typographic apostrophe, dropped markdown, "[...]" elision).
func TestGuardReviewFindings(t *testing.T) {
	batch := []Message{
		{Role: "assistant", Content: "Under AFK I decide its three open questions, taking its safer picks."},
		{Role: "user", Content: "go ahead and do the fix"},
		{Role: "assistant", Content: "Step one: rebuild the profile on a copy of your store."},
		{Role: "assistant", Content: "Shall I also run the review loop again before shipping it?"},
		{Role: "user", Content: "progress?"},
		{Role: "assistant", Content: "Should palate keep the old binary as a rollback copy?"},
		{Role: "user", Content: "ok"},
		{Role: "user", Content: "don't use **bold** for `go test` output, it's noise"},
	}
	cases := []struct {
		name, evidence string
		keep           bool
	}{
		{"C1 proposal paired with a long user line from a different message", `"Under AFK I decide its three open questions" / "don't use bold for go test output"`, false},
		{"C2 reply that is a question is not an approval", `"Shall I also run the review loop again before shipping it" / "progress?"`, false},
		{"C2 proposal from an earlier assistant message than the last one", `"rebuild the profile on a copy of your store" / "progress?"`, false},
		{"C2 approval of the last assistant message", `"Should palate keep the old binary as a rollback copy" / "ok"`, true},
		{"H1 short whole message does not ground a paraphrase", `the user wants rollback copies kept forever / "ok"`, false},
		{"quote variant: curly single quotes and apostrophe", "‘don’t use bold for go test output’", true},
		{"quote variant: guillemets with bracket elision", "«don't use bold for [...] go test output»", true},
	}
	var statements []DistilledStatement
	for _, c := range cases {
		statements = append(statements, DistilledStatement{Statement: "Prefers " + c.name + ".", Category: "General", Confidence: 0.6, Evidence: c.evidence})
	}
	kept, _ := filterAssistantOnlyEvidence(testDB(t), nil, batch, statements)
	got := map[string]bool{}
	for _, s := range kept {
		got[s.Statement] = true
	}
	for _, c := range cases {
		if got["Prefers "+c.name+"."] != c.keep {
			t.Errorf("%s: kept=%v, want %v (evidence %q)", c.name, !c.keep, c.keep, c.evidence)
		}
	}
}

// TestGuardRoundTwo pins the second review: assistant text from the window
// before the batch is still assistant text (N1); a short whole user message
// never grounds a statement by itself (N2); and a reply approves only a
// question line or the final paragraph of the last assistant message, not
// status sentences earlier in a long report.
func TestGuardRoundTwo(t *testing.T) {
	prev := []Message{
		{Role: "assistant", Content: "Earlier I decided to rerun every review round automatically."},
	}
	batch := []Message{
		{Role: "user", Content: "please keep the commit messages short and in English"},
		{Role: "assistant", Content: "The jevd rule is in place for every harness now.\n\n1. Did you turn palate off in Claude Code on purpose?\n\nNext: commit the journal and the handoff together."},
		{Role: "user", Content: "1. yes\n2. go ahead"},
		{Role: "user", Content: "ok"},
	}
	cases := []struct {
		name, evidence string
		keep           bool
	}{
		{"N1 assistant text from the previous window plus a long user line", `"rerun every review round automatically" / "keep the commit messages short and in English"`, false},
		{"N2 short whole user message alone", `"ok"`, false},
		{"status sentence in a long report is not approved", `"The jevd rule is in place for every harness now" / "1. yes"`, false},
		{"question line approved by its reply", `"Did you turn palate off in Claude Code on purpose" / "1. yes"`, true},
		{"final paragraph approved by its reply", `"commit the journal and the handoff together" / "go ahead"`, true},
		{"long user quote still grounds", `"keep the commit messages short"`, true},
	}
	var statements []DistilledStatement
	for _, c := range cases {
		statements = append(statements, DistilledStatement{Statement: "Prefers " + c.name + ".", Category: "General", Confidence: 0.6, Evidence: c.evidence})
	}
	kept, _ := filterAssistantOnlyEvidence(testDB(t), prev, batch, statements)
	got := map[string]bool{}
	for _, s := range kept {
		got[s.Statement] = true
	}
	for _, c := range cases {
		if got["Prefers "+c.name+"."] != c.keep {
			t.Errorf("%s: kept=%v, want %v (evidence %q)", c.name, !c.keep, c.keep, c.evidence)
		}
	}
}

// TestRecommendationBeforeTheFinalParagraphIsApprovable: a reply approves a
// recommendation in the paragraph before the final one (the live case: a
// recommendation, then a closing "Next:" line), while a status paragraph
// earlier in the message stays unapprovable.
func TestRecommendationBeforeTheFinalParagraphIsApprovable(t *testing.T) {
	batch := []Message{
		{Role: "assistant", Content: "The jevd rule is in place for every harness now.\n\nMy recommendation is to run a small test with no code change until it shows Jev beats the current judge.\n\nNext: say go."},
		{Role: "user", Content: "go ahead"},
	}
	statements := []DistilledStatement{
		{Statement: "Prefers a test before code changes.", Category: "Workflow", Confidence: 0.6, Evidence: `"no code change until it shows Jev beats the current judge"; user said "go ahead"`},
		{Statement: "Prefers jevd everywhere.", Category: "Tooling", Confidence: 0.6, Evidence: `"The jevd rule is in place for every harness now" / "go ahead"`},
	}
	kept, _ := filterAssistantOnlyEvidence(testDB(t), nil, batch, statements)
	if len(kept) != 1 || kept[0].Statement != "Prefers a test before code changes." {
		t.Fatalf("kept = %+v, want only the approved recommendation", kept)
	}
}
