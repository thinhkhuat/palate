package distill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
)

// zcodeReader parses ~/.zcode/cli/rollout/model-io-*.jsonl: raw model I/O
// pairs. Only model.role=="main" lines are considered (excludes
// housekeeping calls like title-generation, seen as role:"lite"), and
// request.body.messages is cumulative across lines on live samples, so
// Messages unions by content hash rather than trusting monotonicity
// outright. Experimental: registered only via ExperimentalReaders.
type zcodeReader struct{}

func (zcodeReader) Harness() string { return "zcode" }

func (r zcodeReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = filepath.Join(homeDir(), ".zcode", "cli", "rollout")
	}
	files, err := filepath.Glob(filepath.Join(dir, "model-io-*.jsonl"))
	if err != nil {
		return nil, err
	}
	return statTranscripts(r.Harness(), files, nil)
}

type zcodeLine struct {
	Model struct {
		Role string `json:"role"`
	} `json:"model"`
	Request struct {
		Body struct {
			Messages []zcodeMessage `json:"messages"`
		} `json:"body"`
	} `json:"request"`
}

type zcodeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type zcodeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (zcodeReader) Messages(t Transcript) ([]Message, error) {
	seen := make(map[string]bool)
	var out []Message
	idx := 0
	err := forEachLine(t.Path, func(raw []byte) {
		var l zcodeLine
		if json.Unmarshal(raw, &l) != nil || l.Model.Role != "main" {
			return // e.g. "lite" title-generation calls
		}
		for _, m := range l.Request.Body.Messages {
			if m.Role != "user" && m.Role != "assistant" {
				continue // "system"/"tool" dropped
			}
			text := zcodeMessageText(m.Content)
			if text == "" {
				continue
			}
			key := contentHash(m.Role, text)
			if seen[key] {
				continue // cumulative array — already unioned from an earlier line
			}
			seen[key] = true
			out = append(out, Message{Role: m.Role, Content: text, Index: idx})
			idx++
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// zcodeMessageText extracts learnable text from a raw messages[].content
// field: either a plain string, or a list of blocks (some "reasoning" —
// stripped by construction, the same thinking-exclusion every other
// reader applies).
func zcodeMessageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []zcodeContentBlock
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

func contentHash(role, text string) string {
	h := sha256.Sum256([]byte(role + "\x00" + text))
	return hex.EncodeToString(h[:])
}
