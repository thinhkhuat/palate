package distill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// clineReader parses the Cline CLI's session transcripts:
// ~/.cline/data/sessions/<dir>/<dir>.messages.json — one JSON document whose
// messages[] array is chronological (oldest-first) and whose content blocks
// carry {type:"text",text}, {type:"thinking",thinking}, tool_use/tool_result
// records. Only text blocks are learnable; thinking is stripped by the same
// convention as every other reader, and tool_use/tool_result records are
// noise (the edited file itself is snapshotted by the PostToolUse hook, not
// learned from the transcript).
type clineReader struct{}

func (clineReader) Harness() string { return "cline" }

func (r clineReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = filepath.Join(homeDir(), ".cline", "data", "sessions")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*", "*.messages.json"))
	if err != nil {
		return nil, err
	}
	return statTranscripts(r.Harness(), files, clineProjectOf)
}

// clineProjectOf reads the sibling <dir>.json session record for its cwd /
// workspace_root — the project label the --project filter matches against.
// Best-effort: a missing or unreadable record degrades to "".
func clineProjectOf(path string) string {
	dir := filepath.Dir(path)
	data, err := os.ReadFile(filepath.Join(dir, filepath.Base(dir)+".json"))
	if err != nil {
		return ""
	}
	var meta struct {
		Cwd           string `json:"cwd"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	if meta.Cwd != "" {
		return meta.Cwd
	}
	return meta.WorkspaceRoot
}

type clineDoc struct {
	Messages []clineMsg `json:"messages"`
}

type clineMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type clineBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (clineReader) Messages(t Transcript) ([]Message, error) {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return nil, err
	}
	var doc clineDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil, nil // unparseable transcript — empty corpus, not a hard error
	}
	out := make([]Message, 0, len(doc.Messages))
	for _, m := range doc.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := clineMessageText(m.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, Message{Role: m.Role, Content: text, Index: len(out)})
	}
	return out, nil
}

// clineMessageText extracts learnable text from a content field: either a
// plain string or a list of blocks, keeping only {type:"text"} bodies.
func clineMessageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []clineBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}
