package distill

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// codexReader parses ~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl.
// Each line is one response_item; only payload.type=="message" lines with
// payload.role in (user, assistant) are learnable — "developer" carries
// the system prompt and is always excluded.
type codexReader struct{}

func (codexReader) Harness() string { return "codex" }

func (r codexReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = filepath.Join(homeDir(), ".codex", "sessions")
	}
	var files []string
	if m, err := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*.jsonl")); err == nil {
		files = append(files, m...)
	}
	if m, err := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl")); err == nil {
		files = append(files, m...) // a test fixture dir with the file directly inside
	}
	return statTranscripts(r.Harness(), files, codexCWD)
}

// codexCWD reads only the first line (type=="session_meta") for its cwd —
// cheap, avoids parsing the whole file during Discover.
func codexCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if scanner.Scan() {
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				CWD string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &l) == nil && l.Type == "session_meta" {
			return l.Payload.CWD
		}
	}
	return ""
}

type codexLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string            `json:"type"`
		Role    string            `json:"role"`
		Content []codexContentBlk `json:"content"`
	} `json:"payload"`
}

type codexContentBlk struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (codexReader) Messages(t Transcript) ([]Message, error) {
	var out []Message
	idx := 0
	err := forEachLine(t.Path, func(raw []byte) {
		var l codexLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "response_item" || l.Payload.Type != "message" {
			return
		}
		role := l.Payload.Role
		if role != "user" && role != "assistant" {
			return // "developer" (system prompt carrier) excluded
		}
		var texts []string
		for _, b := range l.Payload.Content {
			// Permissive on block type name: input_text is the only
			// variant directly observed; any block with non-empty text
			// covers unsampled variants (output_text, text, ...).
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) == 0 {
			return
		}
		out = append(out, Message{Role: role, Content: strings.Join(texts, "\n"), Index: idx})
		idx++
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
