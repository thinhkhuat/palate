package distill

import (
	"encoding/json"
	"path/filepath"
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
