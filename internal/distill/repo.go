package distill

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// commitFormat separates the four commit fields with \x1f and each record
// with \x1e — control bytes a commit subject can never itself contain, so
// splitting never ambiguates on spaces or pipes in the subject line.
const commitFormat = "%H%x1f%aI%x1f%aN%x1f%s%x1e"

// Commit is one git commit's identity, mirroring the four fields the
// %x1f/%x1e-delimited --pretty=format walk extracts.
type Commit struct {
	SHA        string
	AuthorDate string
	AuthorName string
	Subject    string
}

// maxOversizedHunkLines is the combined -/+ line-count cap above which a
// hunk is treated as noise (a generated-file rewrite, a mass reformat) and
// dropped rather than turned into a signal.
const maxOversizedHunkLines = 60

// maxSignalTextRunes bounds each side of a GitSignal.Text substitution —
// the same rune-safe truncator internal/learn's diff summarizer uses.
const maxSignalTextRunes = 60

// GitSignal is one before→after substitution pulled from a single diff
// hunk, formatted for BuildUserPrompt's NEW EVIDENCE block.
type GitSignal struct {
	Text     string   // "<removed> → <added>"
	Evidence []string // ["Commit: <subject>", "File: <path>", "In: <context>", "<first hunk lines>"]
}

// CollectCommits walks repoPath's history in two mutually exclusive forms.
//
// When cursorSHA is non-empty, it walks the range cursorSHA..HEAD — a
// range, not a count, so re-running with no new commits since cursorSHA
// returns an empty slice deterministically regardless of total history
// size. If that range fails (the SHA no longer exists, e.g. after a
// rebase or amend), it prints one stderr notice and falls back to the
// no-cursor form below.
//
// With no cursor (first run, or a range fallback), it walks the last N
// commits via -n: N = maxCommits when author is set (git's own -n
// combined with --author already counts only matching commits, so no
// headroom is needed), or 3*maxCommits when author is empty (--all-authors
// — the headroom only matters once nothing filters the walk before the
// count).
//
// Both forms return commits oldest-first, ready for CollectCommits'
// caller to process chronologically and to take the last element's SHA as
// the new cursor position.
func CollectCommits(repoPath, author, cursorSHA string, maxCommits int) ([]Commit, error) {
	if cursorSHA != "" {
		var args []string
		if author != "" {
			args = append(args, "--author="+author)
		}
		args = append(args, cursorSHA+"..HEAD")
		commits, err := runGitLog(repoPath, args...)
		if err == nil {
			reverse(commits)
			return commits, nil
		}
		fmt.Fprintln(os.Stderr, "palate: learn repo: git history rewritten since last cursor — falling back to the last N commits")
	}

	n := maxCommits
	var args []string
	if author != "" {
		args = append(args, "--author="+author)
	} else {
		n = maxCommits * 3
	}
	args = append(args, "-n", strconv.Itoa(n))
	commits, err := runGitLog(repoPath, args...)
	if err != nil {
		return nil, err
	}
	reverse(commits)
	return commits, nil
}

// RepoAuthorEmail returns repoPath's configured git user.email, or an
// error when git config has no user.email set (or the command itself
// fails) — the caller's cue to fall back to an unfiltered walk with its
// own stderr notice.
func RepoAuthorEmail(repoPath string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "-C", repoPath, "config", "user.email")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git config user.email: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	email := strings.TrimSpace(stdout.String())
	if email == "" {
		return "", fmt.Errorf("git config user.email: empty")
	}
	return email, nil
}

// runGitLog shells out to `git -C repoPath log --no-merges --pretty=format:<commitFormat> <extraArgs...>`
// and parses the result. extraArgs are passed as separate exec.Command
// args, never interpolated into a shell string.
func runGitLog(repoPath string, extraArgs ...string) ([]Commit, error) {
	args := []string{"-C", repoPath, "log", "--no-merges", "--pretty=format:" + commitFormat}
	args = append(args, extraArgs...)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseCommits(stdout.String()), nil
}

func parseCommits(out string) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x1f")
		if len(fields) != 4 {
			continue
		}
		commits = append(commits, Commit{
			SHA:        fields[0],
			AuthorDate: fields[1],
			AuthorName: fields[2],
			Subject:    fields[3],
		})
	}
	return commits
}

// reverse flips commits in place — git log yields newest-first, and every
// caller here wants oldest-first (chronological signal order, and the
// last element as the newest/cursor SHA).
func reverse(commits []Commit) {
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
}

// ExtractSignals walks commits oldest-first, running `git show
// --no-color --unified=1 <SHA>` per commit and parsing its unified-diff
// hunks into GitSignals, until either commits run out or maxSignals is
// reached (a running cap across the whole walk, not per-commit). A hunk
// contributes a signal only when it has both a removed and an added side
// (a pure add or pure delete hunk has no "before" or no "after" for the
// before→after shape) and its combined -/+ line count is at most
// maxOversizedHunkLines; a binary file, or one matching a common
// lockfile/generated-file suffix, contributes nothing at all.
func ExtractSignals(repoPath string, commits []Commit, maxSignals int) ([]GitSignal, error) {
	var signals []GitSignal
	for _, c := range commits {
		if len(signals) >= maxSignals {
			break
		}
		out, err := runGitShow(repoPath, c.SHA)
		if err != nil {
			return nil, err
		}
		remaining := maxSignals - len(signals)
		signals = append(signals, parseCommitDiff(out, c.Subject, remaining)...)
	}
	if len(signals) > maxSignals {
		signals = signals[:maxSignals]
	}
	return signals, nil
}

func runGitShow(repoPath, sha string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "-C", repoPath, "show", "--no-color", "--unified=1", sha)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git show %s: %w: %s", sha, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// skippedFileSuffixes marks paths whose diffs are noise, not taste
// evidence: lockfiles, checksum manifests, and minified bundles.
var skippedFileSuffixes = []string{".lock", ".sum", ".min.js"}

func isSkippedFile(path string) bool {
	for _, suf := range skippedFileSuffixes {
		if strings.HasSuffix(path, suf) {
			return true
		}
	}
	return false
}

// parseDiffGitPath extracts the "b/<path>" side of a "diff --git a/<path>
// b/<path>" header line.
func parseDiffGitPath(line string) string {
	const prefix = "diff --git a/"
	rest := strings.TrimPrefix(line, prefix)
	idx := strings.Index(rest, " b/")
	if idx < 0 {
		return rest
	}
	return rest[idx+len(" b/"):]
}

// hunkContext returns the text trailing a "@@ -a,b +c,d @@ <context>"
// header — usually the enclosing function signature — or "" when absent.
func hunkContext(line string) string {
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

// parseCommitDiff parses one `git show --unified=1` transcript into
// GitSignals, stopping once limit signals have been produced (limit is
// the caller's remaining headroom under its own running maxSignals cap).
func parseCommitDiff(out, subject string, limit int) []GitSignal {
	var signals []GitSignal
	var currentFile string
	var skipFile bool
	var inHunk bool
	var context string
	var removed, added, rawHunkLines []string

	flush := func() {
		defer func() { removed, added, rawHunkLines = nil, nil, nil }()
		if !inHunk {
			return
		}
		inHunk = false
		if skipFile || len(removed) == 0 || len(added) == 0 {
			return
		}
		if len(removed)+len(added) > maxOversizedHunkLines {
			return
		}
		removedText := learn.TruncateRunes(strings.Join(removed, " "), maxSignalTextRunes)
		addedText := learn.TruncateRunes(strings.Join(added, " "), maxSignalTextRunes)
		firstLines := rawHunkLines
		if len(firstLines) > 3 {
			firstLines = firstLines[:3]
		}
		signals = append(signals, GitSignal{
			Text: removedText + " → " + addedText,
			Evidence: []string{
				"Commit: " + subject,
				"File: " + currentFile,
				"In: " + context,
				strings.Join(firstLines, "\n"),
			},
		})
	}

	for _, line := range strings.Split(out, "\n") {
		if len(signals) >= limit {
			return signals
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			currentFile = parseDiffGitPath(line)
			skipFile = isSkippedFile(currentFile)
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, "differ"):
			skipFile = true
		case strings.HasPrefix(line, "@@ "):
			flush()
			inHunk = true
			context = hunkContext(line)
		case inHunk && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removed = append(removed, strings.TrimPrefix(line, "-"))
			rawHunkLines = append(rawHunkLines, line)
		case inHunk && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added = append(added, strings.TrimPrefix(line, "+"))
			rawHunkLines = append(rawHunkLines, line)
		}
	}
	flush()
	if len(signals) > limit {
		signals = signals[:limit]
	}
	return signals
}

// FormatGitEvidence renders signals as BuildUserPrompt's NEW EVIDENCE
// block: one "- <Text>" bullet per signal, its Evidence lines indented
// beneath it.
func FormatGitEvidence(signals []GitSignal) string {
	var b strings.Builder
	for i, s := range signals {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- " + s.Text)
		for _, e := range s.Evidence {
			b.WriteString("\n  " + e)
		}
	}
	return b.String()
}

// LearnRepo runs one repo's new commit window (since the "git" cursor at
// repoPath, an absolute path) through ExtractSignals, then Distill +
// ApplyDistilled, and applies the same cursor-advance-only-on-success
// policy LearnSession uses for transcripts: the cursor (source
// "git", key repoPath) moves to the newest processed commit's SHA only
// after a clean Distill; a Distill error instead bumps the window's
// failure streak via RecordFailedAttempt and, once that streak reaches
// maxFailedAttempts, force-advances the cursor past the poisoned window
// anyway. It returns the number of signals extracted alongside the
// session-shaped Summary, since the CLI prints both.
func LearnRepo(ctx context.Context, conn *sql.DB, cfg config.Config, gw *Gateway, repoPath, author string, maxCommits, maxSignals int) (int, Summary, error) {
	return LearnRepoKeyed(ctx, conn, cfg, gw, repoPath, repoPath, author, maxCommits, maxSignals)
}

// LearnRepoKeyed is LearnRepo with the git cursor stored under cursorKey
// instead of repoPath — for a temp clone, the URL it came from.
func LearnRepoKeyed(ctx context.Context, conn *sql.DB, cfg config.Config, gw *Gateway, repoPath, cursorKey, author string, maxCommits, maxSignals int) (int, Summary, error) {
	cursorSHA, _, _ := GetCursor(conn, "git", cursorKey)

	commits, err := CollectCommits(repoPath, author, cursorSHA, maxCommits)
	if err != nil {
		return 0, Summary{}, fmt.Errorf("collect commits: %w", err)
	}
	if len(commits) == 0 {
		return 0, Summary{Skipped: true}, nil
	}

	signals, err := ExtractSignals(repoPath, commits, maxSignals)
	if err != nil {
		return 0, Summary{}, fmt.Errorf("extract signals: %w", err)
	}
	if len(signals) == 0 {
		return 0, Summary{Skipped: true}, nil
	}

	newestSHA := commits[len(commits)-1].SHA

	prefs, err := learn.ListPreferences(conn, false)
	if err != nil {
		return len(signals), Summary{}, fmt.Errorf("list preferences: %w", err)
	}

	rulebookBlock, _ := learn.LoadRulebook(cfg)
	result, distillErr := Distill(ctx, conn, gw, prefs, "", FormatGitEvidence(signals), rulebookBlock)
	if distillErr == nil {
		applied, dropped := ApplyDistilled(conn, cfg, result.Statements)
		if err := SetCursor(conn, "git", cursorKey, newestSHA); err != nil {
			return len(signals), Summary{}, fmt.Errorf("set cursor: %w", err)
		}
		return len(signals), Summary{Applied: applied, Dropped: dropped, Advanced: true}, nil
	}

	attempts, err := RecordFailedAttempt(conn, "git", cursorKey)
	if err != nil {
		return len(signals), Summary{}, fmt.Errorf("record failed attempt: %w", err)
	}
	if attempts < maxFailedAttempts {
		return len(signals), Summary{Reason: distillErr.Error()}, nil
	}

	if err := SetCursor(conn, "git", cursorKey, newestSHA); err != nil {
		return len(signals), Summary{}, fmt.Errorf("set cursor (abandon): %w", err)
	}
	storage.BumpDrop(conn, "llm_distilled_window_abandoned")
	return len(signals), Summary{Reason: distillErr.Error(), Advanced: true, Abandoned: true}, nil
}
