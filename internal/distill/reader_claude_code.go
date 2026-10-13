package distill

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// claudeCodeReader parses Claude Code's ~/.claude/projects/<slug>/*.jsonl
// transcripts. Each line is one JSON object; user turns carry
// message.content as a plain string (command wrappers, tool-result echoes
// and teammate relays are filtered by the leading "<" check below), while
// assistant turns carry message.content as an array of blocks, of which
// only type=="text" is learnable (thinking/tool_use excluded by
// construction).
type claudeCodeReader struct{}

func (claudeCodeReader) Harness() string { return "claude_code" }

// Discover globs root for *.jsonl both directly (a single project's own
// dir — what --project passes, and what a test fixture dir looks like) and
// one level down (root/*/*.jsonl — the unscoped default, every project
// under ~/.claude/projects). Trying both shapes keeps this one Discover
// correct for either caller without a second parameter.
func (r claudeCodeReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude", "projects")
	}
	var files []string
	if m, err := filepath.Glob(filepath.Join(dir, "*.jsonl")); err == nil {
		files = append(files, m...)
	}
	if m, err := filepath.Glob(filepath.Join(dir, "*", "*.jsonl")); err == nil {
		files = append(files, m...)
	}
	return statTranscripts(r.Harness(), files, claudeCodeProjectFromPath)
}

// claudeCodeProjectFromPath derives a best-effort project label from the
// transcript's own directory slug (Claude Code names project dirs by
// replacing "/" with "-" in the absolute cwd) rather than opening every
// file's content — Discover can face over a thousand files in the full
// corpus, so this must stay cheap.
func claudeCodeProjectFromPath(path string) string {
	slug := filepath.Base(filepath.Dir(path))
	return strings.ReplaceAll(strings.TrimPrefix(slug, "-"), "-", "/")
}

type claudeCodeLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeCodeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (claudeCodeReader) Messages(t Transcript) ([]Message, error) {
	var out []Message
	idx := 0
	err := forEachLine(t.Path, func(raw []byte) {
		var l claudeCodeLine
		if json.Unmarshal(raw, &l) != nil {
			return
		}
		switch l.Type {
		case "user":
			var s string
			if json.Unmarshal(l.Message.Content, &s) != nil {
				return // block-array user lines (tool results) are not learnable text
			}
			if s == "" || strings.HasPrefix(s, "<") {
				return // <command-message>/<teammate-message>/<system-reminder>/tool-wrapper text
			}
			if s = userOwnText(s); s == "" {
				idx++  // keep later indexes, and so stored cursors, unchanged
				return // a bare /command, or a side-agent note with no reply typed under it
			}
			out = append(out, Message{Role: "user", Content: s, Index: idx})
			idx++
		case "assistant":
			var blocks []claudeCodeContentBlock
			if json.Unmarshal(l.Message.Content, &blocks) != nil {
				return
			}
			var texts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					texts = append(texts, b.Text)
				}
			}
			if len(texts) == 0 {
				return // thinking/tool_use-only turn
			}
			out = append(out, Message{Role: "assistant", Content: strings.Join(texts, "\n"), Index: idx})
			idx++
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sideAgentLeadIn opens a note the harness pastes into the user turn on a
// side agent's behalf; the note's own lines follow as a "> " block quote.
const sideAgentLeadIn = "Here is a note offered by a side agent"

// slashCommand matches a bare harness command such as "/compact" or
// "/plugin:taste"; a file path ("/Users/me/x.go") is the user's text.
var slashCommand = regexp.MustCompile(`^/[A-Za-z][\w:.-]*$`)

// userOwnText returns the part of a user turn the user typed as taste
// evidence: "" for a bare slash command, and for a turn carrying a side-agent
// note (its lead-in anywhere in the turn) everything except the lead-in line
// and the note's "> " lines.
func userOwnText(s string) string {
	trimmed := strings.TrimSpace(s)
	if slashCommand.MatchString(trimmed) {
		return ""
	}
	if !strings.Contains(trimmed, sideAgentLeadIn) {
		return s
	}
	var own []string
	inNote := false
	for _, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, sideAgentLeadIn):
			inNote = true
		case inNote && strings.HasPrefix(l, ">"):
		default:
			own = append(own, line)
		}
	}
	return strings.TrimSpace(strings.Join(own, "\n"))
}
