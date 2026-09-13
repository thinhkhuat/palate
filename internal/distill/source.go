package distill

import (
	"bufio"
	"bytes"
	"os"
)

// Reader loads Messages from one harness's transcript format.
type Reader interface {
	Harness() string
	Discover(root string) ([]Transcript, error) // root == "" uses the harness default dir
	Messages(t Transcript) ([]Message, error)   // oldest-first, full transcript
}

// AllReaders returns every non-experimental reader, in a fixed, testable
// order. ZCode is deliberately excluded — see ExperimentalReaders.
func AllReaders() []Reader {
	return []Reader{
		claudeCodeReader{},
		newCommandCodeReader(),
		newPiReader(),
		codexReader{},
		openCodeReader{},
		geminiCLIReader{},
		memsearchReader{},
	}
}

// ExperimentalReaders returns readers gated behind --include-experimental.
func ExperimentalReaders() []Reader {
	return []Reader{zcodeReader{}}
}

// homeDir resolves $HOME once per call for a reader's default discovery
// root. A failure degrades to "" — Discover then simply finds nothing,
// which callers already treat like any other empty-corpus result.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// statTranscripts turns a list of file paths into Transcripts, skipping any
// path that no longer stat's (races with an external process) and
// deduplicating paths matched by more than one glob pattern. projectFn is
// optional (nil for harnesses whose format carries no project label).
func statTranscripts(harness string, files []string, projectFn func(path string) string) ([]Transcript, error) {
	seen := make(map[string]bool, len(files))
	var out []Transcript
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		project := ""
		if projectFn != nil {
			project = projectFn(f)
		}
		out = append(out, Transcript{Harness: harness, Project: project, Path: f, ModTime: info.ModTime()})
	}
	return out, nil
}

// forEachLine streams path line by line, handing each non-blank line to fn.
// fn is responsible for skipping a line it cannot parse — forEachLine only
// ever returns a file-level error (the file itself could not be opened or
// read), never a per-line one, which is how every reader satisfies "skip a
// corrupt line and continue" without duplicating the skip logic.
func forEachLine(path string, fn func(line []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // some transcripts embed long lines (diffs, tool output)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		// fn may retain line beyond this call (e.g. gemini's last-line-wins
		// snapshot) — hand it a copy since Scanner reuses its buffer.
		cp := make([]byte, len(line))
		copy(cp, line)
		fn(cp)
	}
	return scanner.Err()
}
