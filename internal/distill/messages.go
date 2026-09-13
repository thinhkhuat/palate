package distill

import (
	"github.com/thinhkhuat/palate/internal/learn"
	"strings"
	"time"
)

// Message is one learnable turn from a transcript, oldest-first.
type Message struct {
	Role    string // "user" | "assistant" — strictly binary, no third value
	Content string
	Index   int // 0-based position among a Reader's emitted Messages; the cursor value
}

// Transcript identifies one session a Reader can load: a file path for
// most harnesses, or "opencode:<db-path>:<session-id>" for OpenCode.
type Transcript struct {
	Harness string
	Project string // best-effort cwd/project label; "" where the source carries none
	Path    string // cursor key
	ModTime time.Time
}

// FormatEvidence renders a window slice into one evidence-block string for
// the distill prompt: one line per message, "User: "/"Assistant: "
// prefixed — except memsearch transcripts, whose lines are always labelled
// "[session summary]" (third-person narration, not raw chat) regardless of
// the message's own stored Role, per the system prompt's own
// [session summary] convention (see prompt.go's SystemPrompt).
// maxMessageRunes caps one message inside an evidence block so a pasted file
// or a long tool narration cannot crowd out the rest of the window.
const maxMessageRunes = 1500

func FormatEvidence(harness string, msgs []Message) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		label := "User"
		switch {
		case harness == "memsearch":
			label = "[session summary]"
		case m.Role == "assistant":
			label = "Assistant"
		}
		lines = append(lines, label+": "+learn.TruncateRunes(m.Content, maxMessageRunes))
	}
	return strings.Join(lines, "\n")
}
