package distill

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// geminiCLIReader parses ~/.gemini/tmp/<hash>/chats/*.jsonl. Each line may
// carry a full "$set.messages" snapshot rather than an append — the last
// line that has one wins (confirmed on a live sample: a later line
// supersedes an earlier one). Only type=="user" entries are extracted; the
// assistant/model role name was never observed live, so extracting it
// would be a guess rather than a verified shape (documented gap, not a
// silent omission — see docs/adapters.md).
type geminiCLIReader struct{}

func (geminiCLIReader) Harness() string { return "gemini_cli" }

func (r geminiCLIReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = filepath.Join(homeDir(), ".gemini", "tmp")
	}
	var files []string
	if m, err := filepath.Glob(filepath.Join(dir, "*", "chats", "*.jsonl")); err == nil {
		files = append(files, m...)
	}
	if m, err := filepath.Glob(filepath.Join(dir, "*.jsonl")); err == nil {
		files = append(files, m...) // a test fixture dir with the file directly inside
	}
	// No project field: Gemini CLI's chat files carry no cwd/project
	// marker, so --project filtering excludes this harness (documented,
	// not silently approximated).
	return statTranscripts(r.Harness(), files, nil)
}

type geminiSetLine struct {
	Set struct {
		Messages []geminiMessage `json:"messages"`
	} `json:"$set"`
}

type geminiMessage struct {
	Type    string `json:"type"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (geminiCLIReader) Messages(t Transcript) ([]Message, error) {
	var last []geminiMessage
	err := forEachLine(t.Path, func(raw []byte) {
		var l geminiSetLine
		if json.Unmarshal(raw, &l) != nil {
			return
		}
		if l.Set.Messages != nil {
			last = l.Set.Messages
		}
	})
	if err != nil {
		return nil, err
	}

	var out []Message
	idx := 0
	for _, m := range last {
		if m.Type != "user" {
			continue
		}
		var texts []string
		for _, c := range m.Content {
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		if len(texts) == 0 {
			continue
		}
		out = append(out, Message{Role: "user", Content: strings.Join(texts, "\n"), Index: idx})
		idx++
	}
	return out, nil
}
