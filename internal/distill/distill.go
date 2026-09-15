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
func ApplyDistilled(conn *sql.DB, cfg config.Config, statements []DistilledStatement) (applied, dropped int) {
	for _, s := range statements {
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
			Type:      learn.SignalLLMDistilled,
			Statement: s.Statement,
			Category:  s.Category,
			Weight:    s.Confidence,
			Evidence:  safety.Censor(s.Evidence),
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
