package distill

import (
	"context"
	"database/sql"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/safety"
	"github.com/thinhkhuat/palate/internal/storage"
)

// distillRetries is the number of extra Gateway.Complete attempts on a
// reply ParseResponse rejects, beyond the first.
const distillRetries = 2

// DistillResult carries a distill pass's outcome: the statements the model
// extracted (nil on failure or when nothing durable was found), plus the
// last raw reply for logging.
type DistillResult struct {
	Statements []DistilledStatement
	RawReply   string
}

// Distill scans and redacts the CURRENT PREFERENCES, PREVIOUSLY ANALYZED,
// and NEW EVIDENCE sections for secrets, assembles the prompt (the curated
// rulebook section, when the caller loaded one, rides along so restatements
// get the "rulebook" flag), calls
// gw.Complete with a ParseResponse-backed validator so a malformed reply
// gets one corrective retry, and parses the (possibly retried) reply.
//
// On final failure — the gateway itself errored, or the last reply still
// doesn't parse — it bumps the llm_distilled_parse counter and returns the
// last raw reply in DistillResult.RawReply alongside the error.
func Distill(ctx context.Context, conn *sql.DB, gw *Gateway, existingPrefs []learn.Preference, previouslyAnalyzedText, newEvidenceText, rulebookBlock string) (DistillResult, error) {
	existingBlock := redactBlock(conn, FormatExistingPreferences(existingPrefs))
	prevBlock := redactBlock(conn, previouslyAnalyzedText)
	newBlock := redactBlock(conn, newEvidenceText)

	userPrompt := BuildUserPrompt(existingBlock, prevBlock, newBlock, rulebookBlock)
	validate := func(raw string) error {
		_, err := ParseResponse(raw)
		return err
	}

	rawReply, err := gw.Complete(ctx, SystemPrompt, userPrompt, distillRetries, validate)
	if err != nil {
		storage.BumpDrop(conn, "llm_distilled_parse")
		return DistillResult{RawReply: rawReply}, err
	}

	statements, err := ParseResponse(rawReply)
	if err != nil {
		storage.BumpDrop(conn, "llm_distilled_parse")
		return DistillResult{RawReply: rawReply}, err
	}
	return DistillResult{Statements: statements, RawReply: rawReply}, nil
}

// redactBlock splits text into lines, runs ScanAndRedactEvidence, bumps
// llm_evidence_secret once per redacted line, and rejoins. An empty block
// (e.g. no CURRENT PREFERENCES yet) passes through unchanged.
func redactBlock(conn *sql.DB, text string) string {
	if text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	redacted, dropped := ScanAndRedactEvidence(lines)
	for i := 0; i < dropped; i++ {
		storage.BumpDrop(conn, "llm_evidence_secret")
	}
	return strings.Join(redacted, "\n")
}

// Judge, when set, stores one distilled statement after judging it against
// the live profile (relation new / same / refines / contradicts) — the write-
// time dedup Command Code's learner gets by amending the taste file in place.
// The CLI sets it to the judge package's Apply (the judge imports this package
// for its Gateway, so the seam points this way); nil keeps learn.ApplySignal's
// heuristic matching, which is what tests and the no-gateway path use.
var Judge func(ctx context.Context, conn *sql.DB, cfg config.Config, s DistilledStatement) error

// ApplyDistilled applies each distilled statement: a safety.Scan flag
// drops it (censored or not — it is never stored), a statement identical
// to its own evidence quote is refused as a verbatim leak of raw source
// text rather than a distilled preference, and everything else routes
// through learn.CreateProposal in review mode or learn.ApplySignal
// otherwise, with the model's own per-statement confidence as the seed
// weight. It returns the counts applied and dropped.
//
// sessionKey is required (phase-03 req 1): it rides every statement's Session
// field into the create/reinforce event, and the judge seam, so
// learn.SessionCounts can tell "seen again in a later session" from "seen twice
// in the same batch". A caller with no real session passes "".
func ApplyDistilled(conn *sql.DB, cfg config.Config, statements []DistilledStatement, session string) (applied, dropped int) {
	for _, s := range statements {
		s.Session = session
		if scan := safety.Scan(s.Statement); scan.Flagged {
			storage.BumpDrop(conn, "llm_distilled_"+scan.Reason)
			dropped++
			continue
		}
		if isVerbatimEvidence(s.Statement, s.Evidence) {
			storage.BumpDrop(conn, "llm_distilled_verbatim")
			dropped++
			continue
		}

		if cfg.Distill.Mode == "review" {
			if _, err := learn.CreateProposal(conn, s.Statement, s.Category); err != nil {
				storage.BumpDrop(conn, "llm_distilled_proposal_error")
				dropped++
				continue
			}
			storage.BumpHit(conn, "llm_distilled")
			applied++
			continue
		}

		if Judge != nil {
			if err := Judge(context.Background(), conn, cfg, s); err != nil {
				storage.BumpDrop(conn, "llm_distilled_apply_error")
				dropped++
				continue
			}
			storage.BumpHit(conn, "llm_distilled")
			applied++
			continue
		}
		sig := learn.Signal{
			Type:            learn.SignalLLMDistilled,
			Statement:       s.Statement,
			Category:        s.Category,
			Weight:          s.Confidence,
			Evidence:        safety.Censor(s.Evidence),
			Session:         s.Session,
			ProjectSpecific: s.Project,
		}
		if _, err := learn.ApplySignal(conn, cfg, sig, nil); err != nil {
			storage.BumpDrop(conn, "llm_distilled_apply_error")
			dropped++
			continue
		}
		storage.BumpHit(conn, "llm_distilled")
		applied++
	}
	return applied, dropped
}

// isVerbatimEvidence reports whether statement is, after trimming, exactly
// the evidence line it claims to be backed by — the model quoting raw
// evidence text back as if it were a distilled preference, rather than
// generalizing it.
func isVerbatimEvidence(statement, evidence string) bool {
	s := strings.TrimSpace(statement)
	e := strings.TrimSpace(evidence)
	return s != "" && s == e
}

// minEvidenceQuoteLen is the shortest (normalised) evidence quote the
// assistant-only filter acts on (phase-03 req 4): a shorter quote matches too
// much text in either speaker's messages to safely attribute to one of them.
const minEvidenceQuoteLen = 12

// filterAssistantOnlyEvidence drops a distilled statement whose evidence
// quote (normalised: lower-cased, whitespace collapsed) appears in an
// assistant message of batch but in no user message of batch — text the
// assistant itself wrote, including its own summaries, is never evidence of
// what the user wants (req 4). A quote shorter than minEvidenceQuoteLen, or
// one that matches no message at all (a paraphrase), is kept: origin cannot
// be told from the quote alone. Every drop bumps
// llm_distilled_assistant_evidence.
func filterAssistantOnlyEvidence(conn *sql.DB, batch []Message, statements []DistilledStatement) (kept []DistilledStatement, dropped int) {
	var userBlob, assistantBlob strings.Builder
	for _, m := range batch {
		norm := normalizeEvidenceText(m.Content)
		if m.Role == "assistant" {
			assistantBlob.WriteString(norm)
			assistantBlob.WriteByte('\n')
			continue
		}
		userBlob.WriteString(norm)
		userBlob.WriteByte('\n')
	}
	user, assistant := userBlob.String(), assistantBlob.String()

	kept = make([]DistilledStatement, 0, len(statements))
	for _, s := range statements {
		quote := normalizeEvidenceText(s.Evidence)
		if len(quote) >= minEvidenceQuoteLen && strings.Contains(assistant, quote) && !strings.Contains(user, quote) {
			storage.BumpDrop(conn, "llm_distilled_assistant_evidence")
			dropped++
			continue
		}
		kept = append(kept, s)
	}
	return kept, dropped
}

// normalizeEvidenceText lower-cases s and collapses its whitespace, so a
// quote and its source text compare on content, not on incidental spacing or
// case.
func normalizeEvidenceText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
