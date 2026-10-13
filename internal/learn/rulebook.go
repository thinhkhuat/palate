package learn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/safety"
)

// RulebookOverlap is the provenance tag marking a preference row that merely
// restates a rule from the user's curated rulebook (config key `rulebook:`).
// The rulebook is authoritative and always loaded, so the learned copy only
// burns render tokens: render suppresses tagged rows and the consolidator
// retires them. The tag rides the existing provenance JSON array (the same
// mechanism merge uses) so no schema change and no scan churn is needed.
const RulebookOverlap = "rulebook_overlap"

// rulebookCeilingBytes is a safety ceiling on the whole assembled rulebook
// (root file plus every followed @import), replacing the old 8 KB
// per-file cap (phase-03 req 5, design §6.8): the owner's real, always-loaded
// rulebook (~/.claude/CLAUDE.md importing @RULES.md) measured 33,569 bytes on
// 2026-09-27, so this ceiling — about 7.6x that — never cuts it; it only
// stops a pathological paste.
const rulebookCeilingBytes = 256 << 10

// maxImportDepth bounds @import recursion. A cycle guard (visited, keyed by
// each file's resolved absolute path) also applies independently, so a
// shallow A->B->A cycle terminates well before this depth is ever reached.
const maxImportDepth = 5

// LoadRulebook returns the curated rulebook as one rule per line: the root
// file plus every file it imports via an "@<path>" line, recursively (req
// 5). Every line is run through the secret scanner (redacted like evidence —
// nothing secret-shaped leaves the machine via the rulebook either), and the
// assembled whole is capped at rulebookCeilingBytes. Fail-open: an unset,
// unreadable, or empty rulebook returns ("", false) and every caller treats
// the feature as off.
func LoadRulebook(cfg config.Config) (string, bool) {
	if strings.TrimSpace(cfg.Rulebook) == "" {
		return "", false
	}
	lines, ok := loadRulebookFile(cfg.Rulebook, 0, map[string]bool{})
	if !ok {
		return "", false
	}
	block := strings.Join(lines, "\n")
	if len(block) > rulebookCeilingBytes {
		fmt.Fprintf(os.Stderr, "palate: rulebook exceeds %d bytes after imports; truncating at a line boundary\n", rulebookCeilingBytes)
		block = block[:rulebookCeilingBytes]
		if idx := strings.LastIndex(block, "\n"); idx > 0 {
			block = block[:idx]
		}
		block = strings.TrimSpace(block)
	}
	if block == "" {
		return "", false
	}
	return block, true
}

// loadRulebookFile reads one rulebook file's lines, following an "@<path>"
// import line recursively: the path is relative to the importing file's own
// directory, "~" expands to the user's home, depth is capped at
// maxImportDepth, and a file already on the current chain (a cycle) is
// silently skipped rather than re-included or hung on. An unreadable import
// is skipped with one stderr line — it must never fail the rest of the
// rulebook. The root file itself failing to read is the one case that fails
// the whole call: ok is false only then.
func loadRulebookFile(path string, depth int, visited map[string]bool) ([]string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if visited[abs] {
		return nil, true
	}
	visited[abs] = true

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	rawLines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue // blank lines carry no rule and only grow the prompt
		}
		if importPath, isImport := parseImportLine(line); isImport {
			if depth >= maxImportDepth {
				fmt.Fprintf(os.Stderr, "palate: rulebook import depth exceeded, skipping %q\n", importPath)
				continue
			}
			resolved := resolveImportPath(importPath, filepath.Dir(abs))
			imported, ok := loadRulebookFile(resolved, depth+1, visited)
			if !ok {
				fmt.Fprintf(os.Stderr, "palate: rulebook import unreadable, skipping %q\n", importPath)
				continue
			}
			out = append(out, imported...)
			continue
		}
		if safety.ContainsSecret(line) {
			out = append(out, "[redacted secret]")
			continue
		}
		out = append(out, line)
	}
	return out, true
}

// parseImportLine reports whether line's first whitespace-delimited token
// starts with "@" — an import line — and returns that token with the "@"
// stripped.
func parseImportLine(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	first := fields[0]
	if !strings.HasPrefix(first, "@") || len(first) < 2 {
		return "", false
	}
	return first[1:], true
}

// resolveImportPath expands a leading "~" to the user's home directory, then
// resolves the result relative to baseDir (the importing file's own
// directory) unless it is already absolute.
func resolveImportPath(importPath, baseDir string) string {
	p := importPath
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = home
		}
	} else if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

// HasRulebookOverlap reports whether a preference's provenance carries the
// rulebook_overlap tag. Provenance is a JSON array of strings, so the tag is
// matched as a quoted JSON string — the same shape merge writes via
// json_insert.
func HasRulebookOverlap(provenance string) bool {
	return strings.Contains(provenance, `"`+RulebookOverlap+`"`)
}
