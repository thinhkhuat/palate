package distill

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"unicode/utf8"

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

// minEvidenceQuoteLen is the shortest (normalised) evidence piece that is
// classified as user or assistant text: a shorter piece matches too much text
// by chance. A shorter piece matters only as an approval line ("1. yes").
const minEvidenceQuoteLen = 12

// evidenceSplitter breaks the model's evidence field into the quoted pieces it
// is built from: the learner joins quotes with " / ", elides them with "...",
// "…" or "[...]", wraps them in straight, curly or guillemet quotes, and
// annotates them in parentheses. Single quotes are not split on, because they
// double as apostrophes; normalizeEvidenceText folds curly ones to ' and
// trimEvidenceEdges strips them at a piece's edges.
var evidenceSplitter = regexp.MustCompile(`["“”«»()]| / |\[(?:\.\.\.|…)\]|\.\.\.|…`)

// listMarker matches a numbered-list prefix on a reply line ("1. ", "2) ").
var listMarker = regexp.MustCompile(`^\d+[.)]\s*`)

// maxApprovalRunes is the longest user reply line that can approve the
// assistant message before it; a longer line states something itself.
const maxApprovalRunes = 60

// filterAssistantOnlyEvidence keeps a distilled statement only when its
// evidence is grounded in what the user typed in batch. The evidence is split
// into its quoted pieces, each normalised (lower-cased, whitespace collapsed,
// curly apostrophes folded, markdown backticks and asterisks removed, a
// leading "user:"/"assistant:" label and edge punctuation removed). A long
// piece (minEvidenceQuoteLen or more) is user text when a user message of
// batch contains it, otherwise assistant text when an assistant message of
// prev or batch contains it (the model sees both windows).
//
//   - Evidence with any assistant piece is grounded only as an approval: one
//     piece equals a short reply line of a user message (at most
//     maxApprovalRunes, not a /command, not a question), and every assistant
//     piece is in what that reply can approve: a line ending in a question,
//     or one of the last two paragraphs, of the last assistant message before it.
//   - Otherwise it is grounded only by a long user piece.
//
// Everything else is dropped (fail closed). A drop with an assistant piece
// bumps llm_distilled_assistant_evidence; any other drop bumps
// llm_distilled_ungrounded_evidence.
func filterAssistantOnlyEvidence(conn *sql.DB, prev, batch []Message, statements []DistilledStatement) (kept []DistilledStatement, dropped int) {
	type userTurn struct {
		approvable string          // question lines and final paragraph of the assistant message before it
		approvals  map[string]bool // reply lines short enough to approve it
	}
	var userBlob, assistantBlob strings.Builder
	for _, m := range prev {
		if m.Role == "assistant" {
			assistantBlob.WriteString(normalizeEvidenceText(m.Content) + "\n")
		}
	}
	var turns []userTurn
	lastAssistant := ""
	for _, m := range batch {
		if m.Role == "assistant" {
			assistantBlob.WriteString(normalizeEvidenceText(m.Content) + "\n")
			lastAssistant = m.Content
			continue
		}
		userBlob.WriteString(normalizeEvidenceText(m.Content) + "\n")
		approvals := map[string]bool{}
		for _, raw := range strings.Split(m.Content, "\n") {
			l := normalizeEvidenceText(raw)
			if l == "" || strings.HasPrefix(l, "/") || strings.HasSuffix(l, "?") {
				continue
			}
			if l = trimEvidenceEdges(l); l != "" && utf8.RuneCountInString(l) <= maxApprovalRunes {
				approvals[l] = true
				approvals[trimEvidenceEdges(listMarker.ReplaceAllString(l, ""))] = true // "2. go ahead" also as "go ahead"
			}
		}
		if lastAssistant != "" {
			turns = append(turns, userTurn{approvable: approvableText(lastAssistant), approvals: approvals})
		}
		lastAssistant = ""
	}
	user, assistant := userBlob.String(), assistantBlob.String()

	kept = make([]DistilledStatement, 0, len(statements))
	for _, s := range statements {
		var frags, fromUser, fromAssistant []string
		for _, piece := range evidenceSplitter.Split(s.Evidence, -1) {
			f := trimEvidenceEdges(normalizeEvidenceText(piece))
			if f == "" {
				continue
			}
			frags = append(frags, f)
			if len(f) < minEvidenceQuoteLen {
				continue
			}
			switch {
			case strings.Contains(user, f):
				fromUser = append(fromUser, f)
			case strings.Contains(assistant, f):
				fromAssistant = append(fromAssistant, f)
			}
		}
		grounded := len(fromAssistant) == 0 && len(fromUser) > 0
		for _, turn := range turns {
			if grounded || len(fromAssistant) == 0 {
				break
			}
			approved := false
			for _, f := range frags {
				approved = approved || turn.approvals[f]
			}
			if !approved {
				continue
			}
			grounded = true
			for _, a := range fromAssistant {
				grounded = grounded && strings.Contains(turn.approvable, a)
			}
		}
		if grounded {
			kept = append(kept, s)
			continue
		}
		if len(fromAssistant) > 0 {
			storage.BumpDrop(conn, "llm_distilled_assistant_evidence")
		} else {
			storage.BumpDrop(conn, "llm_distilled_ungrounded_evidence")
		}
		dropped++
	}
	return kept, dropped
}

// approvableText returns, normalised, the parts of an assistant message a short
// user reply can approve: every line holding a question, and the last two
// paragraphs (a recommendation and the closing "Next:" step). Status
// sentences earlier in a long report are left out.
func approvableText(msg string) string {
	var b strings.Builder
	for _, line := range strings.Split(msg, "\n") {
		if strings.Contains(line, "?") {
			b.WriteString(normalizeEvidenceText(line) + "\n")
		}
	}
	paras := strings.Split(strings.TrimSpace(msg), "\n\n")
	for _, p := range paras[max(0, len(paras)-2):] {
		b.WriteString(normalizeEvidenceText(p) + "\n")
	}
	return b.String()
}

// trimEvidenceEdges removes a leading speaker label ("user:", "assistant:")
// and the punctuation and spaces around a normalised evidence fragment.
func trimEvidenceEdges(s string) string {
	s = strings.TrimSpace(s)
	for _, label := range []string{"user:", "assistant:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, label))
	}
	return strings.Trim(s, " .,;:!?—–-'`")
}

// normalizeEvidenceText lower-cases s and collapses its whitespace, so a
// quote and its source text compare on content, not on incidental spacing or
// case.
func normalizeEvidenceText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(evidenceFolder.Replace(s))), " ")
}

// evidenceFolder folds the characters a model changes when it quotes the
// user: curly apostrophes become ', and markdown backticks and asterisks the
// model drops are removed from both sides.
var evidenceFolder = strings.NewReplacer("’", "'", "‘", "'", "`", "", "*", "")
