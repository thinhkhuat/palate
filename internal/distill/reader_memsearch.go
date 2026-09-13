package distill

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// memsearchReader parses .memsearch/memory/*.md journal files: "## Session
// HH:MM" section headers and, within them, "### HH:MM" blocks whose bullet
// lines become one Message each. Discover defaults to the project-local
// glob (cwd-relative); the caller passes an explicit root (the global
// ~/.memsearch/memory dir) to opt into scanning outside the project.
type memsearchReader struct{}

func (memsearchReader) Harness() string { return "memsearch" }

func (r memsearchReader) Discover(root string) ([]Transcript, error) {
	dir := root
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(cwd, ".memsearch", "memory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	// No project field: journals are scoped by directory (project-local vs
	// global), not by an in-file marker — --project filtering excludes
	// this harness (documented, not silently approximated).
	return statTranscripts(r.Harness(), files, nil)
}

var memsearchTimeHeader = regexp.MustCompile(`^###\s+\d{2}:\d{2}\s*$`)

func (memsearchReader) Messages(t Transcript) ([]Message, error) {
	f, err := os.Open(t.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Message
	idx := 0
	var bullets []string
	inBlock := false
	flush := func() {
		if inBlock && len(bullets) > 0 {
			out = append(out, Message{Role: "user", Content: strings.Join(bullets, "\n"), Index: idx})
			idx++
		}
		bullets = nil
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		switch {
		case memsearchTimeHeader.MatchString(trimmed):
			flush()
			inBlock = true
		case strings.HasPrefix(trimmed, "## "):
			flush()
			inBlock = false
		case strings.HasPrefix(trimmed, "<!--"):
			// the session/turn/transcript comment line, not a bullet
		case inBlock && strings.HasPrefix(trimmed, "-"):
			bullets = append(bullets, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
