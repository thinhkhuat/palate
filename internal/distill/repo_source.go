package distill

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ownerRepo matches GitHub's short form, the same spelling Command Code's
// `cmd taste learn owner/repo` accepts.
var ownerRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// RepoSource classifies a `learn repo` argument. A path that exists on disk
// is local, whatever it looks like. Otherwise `owner/repo` becomes the GitHub
// HTTPS URL, and an https://, ssh://, git@ or .git spelling is a remote URL as
// given. cursorKey is what the git cursor is stored under: the local absolute
// path, or the URL for a remote — a fresh clone lands in a new temp dir every
// time, so the temp path would never match.
func RepoSource(arg string) (cloneURL string, remote bool) {
	if arg == "" {
		return "", false
	}
	if _, err := os.Stat(arg); err == nil {
		return "", false
	}
	switch {
	case strings.HasPrefix(arg, "https://"), strings.HasPrefix(arg, "http://"), strings.HasPrefix(arg, "ssh://"), strings.HasPrefix(arg, "git@"):
		return arg, true
	case strings.HasSuffix(arg, ".git"):
		return arg, true
	case ownerRepo.MatchString(arg):
		return "https://github.com/" + arg + ".git", true
	}
	return "", false
}

// CloneSource clones url into a fresh temp dir with the full history the
// correction-diff mining needs (no --depth) and returns the dir plus its
// remover. Private repos authenticate through git's own credential helper,
// the same way `gh` does.
func CloneSource(ctx context.Context, url string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "palate-learn-repo-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--no-checkout", "--", url, dir)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git clone %s: %w: %s", url, err, strings.TrimSpace(stderr.String()))
	}
	return dir, cleanup, nil
}
