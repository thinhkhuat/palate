package distill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// maxFailedAttempts is how many consecutive transient failures a window
// tolerates before the cursor force-advances and the window is abandoned.
const maxFailedAttempts = 3

// batchSize bounds how many new messages one gateway call sees. A transcript
// with a long backlog (first run, or a session that grew a lot between hook
// firings) is walked in consecutive batches, each with its own context window
// and its own cursor advance, instead of one oversized prompt the model
// answers with an empty list.
const batchSize = 20

// Summary reports one LearnSession outcome, aggregated over every batch.
type Summary struct {
	Applied   int
	Dropped   int
	Batches   int    // gateway calls made
	Skipped   bool   // no new messages — the gateway was never called
	Advanced  bool   // the cursor moved (clean success, or forced abandon)
	Abandoned bool   // force-advanced after maxFailedAttempts consecutive failures
	Reason    string // non-empty on a non-advancing failure or a forced abandon
}

// LearnSession walks a transcript's new messages in batches of batchSize
// through Distill + ApplyDistilled and applies the cursor-advance policy per
// batch: the cursor moves to the batch's last message only on a clean
// Distill (ApplyDistilled itself never fails the batch — a bad individual
// statement is already counted in Dropped). On a Distill error
// (gateway/DB/parse), the cursor stays put, RecordFailedAttempt bumps the
// window's failure streak and the walk stops; once that streak reaches
// maxFailedAttempts, the cursor force-advances past the poisoned batch and
// llm_distilled_window_abandoned is bumped so the loss is counted, not
// retried forever. The next run resumes from wherever the cursor stopped.
func LearnSession(ctx context.Context, conn *sql.DB, cfg config.Config, gw *Gateway, reader Reader, t Transcript) (Summary, error) {
	msgs, err := reader.Messages(t)
	if err != nil {
		return Summary{}, fmt.Errorf("read messages: %w", err)
	}

	source, key := t.Harness, t.Path
	// The session key identifies this transcript across runs (phase-03 req
	// 1): every create/reinforce event this session produces carries it, so
	// learn.SessionCounts can tell "seen again in a later session" from
	// "seen twice in the same batch". It never leaves the transcript path in
	// the clear — only a short hash of it.
	sessionKey := sessionKeyFor(source, key)
	// The curated rulebook rides along on every batch so restatements get
	// the rulebook flag; fail-open when unset or unreadable.
	rulebookBlock, _ := learn.LoadRulebook(cfg)
	cursorIndex := -1
	if pos, _, ok := GetCursor(conn, source, key); ok {
		if parsed, err := strconv.Atoi(pos); err == nil {
			cursorIndex = parsed
		}
	}

	var sum Summary
	for {
		prev, newMsgs := Window(msgs, cursorIndex)
		if len(newMsgs) == 0 {
			if sum.Batches == 0 {
				sum.Skipped = true
			}
			return sum, nil
		}
		batch := newMsgs
		if len(batch) > batchSize {
			batch = batch[:batchSize]
			// Context for a mid-backlog batch is the batchSize messages just
			// before it, not the whole prefix.
			prev = contextBefore(msgs, batch[0].Index)
		}
		lastIndex := batch[len(batch)-1].Index

		// The compare set — the profile plus a bounded FTS slice — replaces
		// the whole live ledger here (phase-03 req 2, review F2): the prompt
		// no longer grows with the ledger.
		batchText := FormatEvidence(t.Harness, batch)
		compareSet, err := learn.CompareSet(conn, cfg, batchText)
		if err != nil {
			return sum, fmt.Errorf("compare set: %w", err)
		}

		result, distillErr := Distill(ctx, conn, gw, compareSet, FormatEvidence(t.Harness, prev), batchText, rulebookBlock)
		sum.Batches++
		if distillErr == nil {
			// A statement whose evidence quote lives only in an assistant
			// message (never in a user one) is dropped before it can ever
			// reach ApplyDistilled (phase-03 req 4).
			kept, filtered := filterAssistantOnlyEvidence(conn, batch, result.Statements)
			applied, dropped := ApplyDistilled(conn, cfg, kept, sessionKey)
			sum.Applied += applied
			sum.Dropped += dropped + filtered
			if err := SetCursor(conn, source, key, strconv.Itoa(lastIndex)); err != nil {
				return sum, fmt.Errorf("set cursor: %w", err)
			}
			sum.Advanced = true
			cursorIndex = lastIndex
			continue
		}

		attempts, err := RecordFailedAttempt(conn, source, key)
		if err != nil {
			return sum, fmt.Errorf("record failed attempt: %w", err)
		}
		sum.Reason = distillErr.Error()
		if attempts < maxFailedAttempts {
			return sum, nil
		}
		if err := SetCursor(conn, source, key, strconv.Itoa(lastIndex)); err != nil {
			return sum, fmt.Errorf("set cursor (abandon): %w", err)
		}
		storage.BumpDrop(conn, "llm_distilled_window_abandoned")
		sum.Advanced, sum.Abandoned = true, true
		return sum, nil
	}
}

// contextBefore returns up to batchSize messages immediately preceding the
// message with the given index.
func contextBefore(all []Message, firstIndex int) []Message {
	end := len(all)
	for i, m := range all {
		if m.Index >= firstIndex {
			end = i
			break
		}
	}
	start := end - batchSize
	if start < 0 {
		start = 0
	}
	return all[start:end]
}

// sessionKeyFor computes a transcript's session key (phase-03 req 1):
// harness + ":" + a short hash of its path — stable across runs (the same
// transcript always yields the same key) but never leaks the path itself
// into an event payload or a gateway prompt.
func sessionKeyFor(harness, path string) string {
	return harness + ":" + shortSHA256Hex(path)
}

// shortSHA256Hex hex-encodes the first 12 characters of sha256(s) — shared by
// sessionKeyFor here and repo.go's gitSessionKey, both of which need a short,
// deterministic, non-reversible identity derived from a string that must
// never itself leave the machine.
func shortSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
