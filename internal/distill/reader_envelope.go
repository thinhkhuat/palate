package distill

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// envelopeReader implements Reader for Command Code and Pi, whose JSONL
// envelope is byte-identical on live samples: a type=="session" line
// carries the transcript's cwd, type=="message" lines carry role and
// content[] blocks, and other line types ("custom", ...) are skipped.
type envelopeReader struct {
	harness    string
	defaultDir func() string
}

func newEnvelopeReader(harness string, defaultDir func() string) envelopeReader {
	return envelopeReader{harness: harness, defaultDir: defaultDir}
}

func (r envelopeReader) Harness() string { return r.harness }

func (r envelopeReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		dir = r.defaultDir()
	}
	var files []string
	if m, err := filepath.Glob(filepath.Join(dir, "*.jsonl")); err == nil {
		files = append(files, m...)
	}
	if m, err := filepath.Glob(filepath.Join(dir, "*", "*.jsonl")); err == nil {
		files = append(files, m...)
	}
	filtered := files[:0]
	for _, f := range files {
		if strings.HasSuffix(f, ".checkpoints.jsonl") {
			continue
		}
		filtered = append(filtered, f)
	}
	return statTranscripts(r.harness, filtered, envelopeCWD)
}

// envelopeCWD reads just the first line's cwd — cheap, avoids parsing the
// whole file during Discover.
func envelopeCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if scanner.Scan() {
		var l struct {
			Type string `json:"type"`
			CWD  string `json:"cwd"`
		}
		if json.Unmarshal(scanner.Bytes(), &l) == nil && l.Type == "session" {
			return l.CWD
		}
	}
	return ""
}

type envelopeLine struct {
	Type    string `json:"type"`
	Message struct {
		Role    string            `json:"role"`
		Content []envelopeContent `json:"content"`
	} `json:"message"`
}

type envelopeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r envelopeReader) Messages(t Transcript) ([]Message, error) {
	var out []Message
	idx := 0
	err := forEachLine(t.Path, func(raw []byte) {
		var l envelopeLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "message" {
			return
		}
		role := l.Message.Role
		if role != "user" && role != "assistant" {
			return
		}
		var texts []string
		for _, b := range l.Message.Content {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) == 0 {
			return // e.g. a tool_result/tool_use-only turn
		}
		out = append(out, Message{Role: role, Content: strings.Join(texts, "\n"), Index: idx})
		idx++
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
