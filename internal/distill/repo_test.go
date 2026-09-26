package distill

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

// isolateGitEnv points every git config layer (system, global, XDG) at a
// throwaway location so a test's git commands never see this machine's
// real ~/.gitconfig (which has a real user.email) — required for the
// "unset user.email" scenarios to be deterministic in any environment.
func isolateGitEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "empty-gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(home, "empty-gitconfig"))
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.String()
}

func newFixtureRepo(t *testing.T) string {
	t.Helper()
	isolateGitEnv(t)
	dir := t.TempDir()
	runGitCmd(t, dir, "-c", "init.defaultBranch=main", "init", "-q")
	return dir
}

// commitFileAs writes relPath with content and commits it under the given
// author identity, returning the new commit's SHA.
func commitFileAs(t *testing.T, dir, relPath string, content []byte, subject, email, name string) string {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", relPath)
	runGitCmd(t, dir, "-c", "user.email="+email, "-c", "user.name="+name, "-c", "commit.gpgsign=false", "commit", "-q", "-m", subject)
	return strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD"))
}

func commitFile(t *testing.T, dir, relPath string, content []byte, subject string) string {
	t.Helper()
	return commitFileAs(t, dir, relPath, content, subject, "test@example.com", "test")
}

func renameFile(t *testing.T, dir, from, to, subject string) string {
	t.Helper()
	runGitCmd(t, dir, "mv", from, to)
	runGitCmd(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false", "commit", "-q", "-m", subject)
	return strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD"))
}

// captureStderr runs fn with os.Stderr swapped for a pipe and returns
// whatever fn wrote to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	captured, _ := io.ReadAll(r)
	return string(captured)
}

func shas(commits []Commit) []string {
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.SHA
	}
	return out
}

// --- CollectCommits ---

func TestCollectCommitsNoCursorOldestFirst(t *testing.T) {
	dir := newFixtureRepo(t)
	c1 := commitFile(t, dir, "a.txt", []byte("one\n"), "first")
	c2 := commitFile(t, dir, "b.txt", []byte("two\n"), "second")
	c3 := commitFile(t, dir, "c.txt", []byte("three\n"), "third")

	commits, err := CollectCommits(dir, "test@example.com", "", 10)
	if err != nil {
		t.Fatalf("CollectCommits: %v", err)
	}
	got := shas(commits)
	want := []string{c1, c2, c3}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CollectCommits SHAs = %v, want oldest-first %v", got, want)
	}
	if commits[2].Subject != "third" {
		t.Fatalf("newest commit Subject = %q, want %q", commits[2].Subject, "third")
	}
}

func TestCollectCommitsNoCursorRespectsWindow(t *testing.T) {
	dir := newFixtureRepo(t)
	for i := 0; i < 5; i++ {
		commitFile(t, dir, "f"+strconv.Itoa(i)+".txt", []byte("x\n"), "commit "+strconv.Itoa(i))
	}
	commits, err := CollectCommits(dir, "test@example.com", "", 2)
	if err != nil {
		t.Fatalf("CollectCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2 (maxCommits window)", len(commits))
	}
	// -n keeps the newest N; oldest-first means the last two chronologically.
	if commits[1].Subject != "commit 4" {
		t.Fatalf("newest returned commit = %q, want %q", commits[1].Subject, "commit 4")
	}
}

func TestCollectCommitsRangeEmptyAtHead(t *testing.T) {
	dir := newFixtureRepo(t)
	commitFile(t, dir, "a.txt", []byte("one\n"), "first")
	head := commitFile(t, dir, "b.txt", []byte("two\n"), "second")

	commits, err := CollectCommits(dir, "", head, 10)
	if err != nil {
		t.Fatalf("CollectCommits: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("commits at cursor==HEAD = %d, want 0", len(commits))
	}
}

func TestCollectCommitsRangeReturnsNewOnly(t *testing.T) {
	dir := newFixtureRepo(t)
	c1 := commitFile(t, dir, "a.txt", []byte("one\n"), "first")
	c2 := commitFile(t, dir, "b.txt", []byte("two\n"), "second")
	c3 := commitFile(t, dir, "c.txt", []byte("three\n"), "third")

	commits, err := CollectCommits(dir, "", c1, 10)
	if err != nil {
		t.Fatalf("CollectCommits: %v", err)
	}
	got := shas(commits)
	want := []string{c2, c3}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CollectCommits range SHAs = %v, want %v", got, want)
	}
}

func TestCollectCommitsFallbackOnRewrittenCursor(t *testing.T) {
	dir := newFixtureRepo(t)
	c1 := commitFile(t, dir, "a.txt", []byte("one\n"), "first")
	commitFile(t, dir, "b.txt", []byte("two\n"), "second")

	// A cursor SHA that never existed in this repo simulates the effect of
	// a rebase/amend that dropped the original commit object entirely.
	vanished := "deadbeef00112233445566778899aabbccddeeff"
	if vanished == c1 {
		t.Fatal("fixture collision")
	}

	var commits []Commit
	var err error
	stderrOut := captureStderr(t, func() {
		commits, err = CollectCommits(dir, "", vanished, 10)
	})
	if err != nil {
		t.Fatalf("CollectCommits: %v, want fallback success", err)
	}
	if len(commits) != 2 {
		t.Fatalf("fallback commits = %d, want 2 (the -n window)", len(commits))
	}
	if !strings.Contains(stderrOut, "falling back to the last N commits") {
		t.Fatalf("stderr = %q, want a fallback notice", stderrOut)
	}
	lines := strings.Split(strings.TrimRight(stderrOut, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr lines = %d, want exactly 1: %q", len(lines), stderrOut)
	}
}

// --- ExtractSignals ---

func TestExtractSignalsSubstitution(t *testing.T) {
	dir := newFixtureRepo(t)
	commitFile(t, dir, "f.txt", []byte("alpha\nbeta\ngamma\n"), "add f")
	c2 := commitFile(t, dir, "f.txt", []byte("alpha\nBETA\ngamma\n"), "tweak beta")

	signals, err := ExtractSignals(dir, []Commit{{SHA: c2, Subject: "tweak beta"}}, 50)
	if err != nil {
		t.Fatalf("ExtractSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(signals))
	}
	s := signals[0]
	if !strings.Contains(s.Text, "beta") || !strings.Contains(s.Text, "BETA") || !strings.Contains(s.Text, "→") {
		t.Fatalf("Text = %q, want a before → after substitution", s.Text)
	}
	if len(s.Evidence) != 4 {
		t.Fatalf("Evidence len = %d, want 4", len(s.Evidence))
	}
	if s.Evidence[0] != "Commit: tweak beta" {
		t.Fatalf("Evidence[0] = %q, want commit subject line", s.Evidence[0])
	}
	if s.Evidence[1] != "File: f.txt" {
		t.Fatalf("Evidence[1] = %q, want file line", s.Evidence[1])
	}
}

func TestExtractSignalsSkipsBinaryAndOversizedAndRename(t *testing.T) {
	dir := newFixtureRepo(t)

	// substitution: contributes one signal.
	commitFile(t, dir, "sub.txt", []byte("alpha\nbeta\ngamma\n"), "add sub")
	subCommit := commitFile(t, dir, "sub.txt", []byte("alpha\nBETA\ngamma\n"), "tweak sub")

	// binary file: contributes nothing.
	binContent := []byte("a\x00b\x00c\x00d")
	binCommit := commitFile(t, dir, "bin.dat", binContent, "add binary")

	// oversized hunk: 40 lines all replaced (80 combined) in one hunk.
	var before, after strings.Builder
	for i := 0; i < 40; i++ {
		before.WriteString("line " + strconv.Itoa(i) + "\n")
		after.WriteString("LINE " + strconv.Itoa(i) + "\n")
	}
	commitFile(t, dir, "big.txt", []byte(before.String()), "add big")
	oversizedCommit := commitFile(t, dir, "big.txt", []byte(after.String()), "rewrite big")

	// pure rename, no content change: contributes nothing (no hunk at all).
	renameCommit := renameFile(t, dir, "sub.txt", "sub-renamed.txt", "rename sub")

	// pure addition: contributes nothing (no removed side).
	pureAddCommit := commitFile(t, dir, "new.txt", []byte("only added\nmore added\n"), "pure add")

	commits := []Commit{
		{SHA: subCommit, Subject: "tweak sub"},
		{SHA: binCommit, Subject: "add binary"},
		{SHA: oversizedCommit, Subject: "rewrite big"},
		{SHA: renameCommit, Subject: "rename sub"},
		{SHA: pureAddCommit, Subject: "pure add"},
	}

	signals, err := ExtractSignals(dir, commits, 50)
	if err != nil {
		t.Fatalf("ExtractSignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %d, want 1 (only the substitution survives)", len(signals))
	}
	if signals[0].Evidence[0] != "Commit: tweak sub" {
		t.Fatalf("surviving signal = %+v, want the substitution commit", signals[0])
	}
}

func TestExtractSignalsCapsAtMaxSignals(t *testing.T) {
	dir := newFixtureRepo(t)
	commitFile(t, dir, "f.txt", []byte("a\nb\nc\n"), "add f")

	var commits []Commit
	for i := 0; i < 5; i++ {
		sha := commitFile(t, dir, "f.txt", []byte("a\nb"+strconv.Itoa(i)+"\nc\n"), "tweak "+strconv.Itoa(i))
		commits = append(commits, Commit{SHA: sha, Subject: "tweak " + strconv.Itoa(i)})
	}

	signals, err := ExtractSignals(dir, commits, 3)
	if err != nil {
		t.Fatalf("ExtractSignals: %v", err)
	}
	if len(signals) != 3 {
		t.Fatalf("signals = %d, want capped at 3", len(signals))
	}
}

// --- author default / --all-authors ---

func TestRepoAuthorDefault(t *testing.T) {
	dir := newFixtureRepo(t)
	runGitCmd(t, dir, "config", "user.email", "me@example.com")

	mine := commitFileAs(t, dir, "mine.txt", []byte("x\n"), "my commit", "me@example.com", "me")
	theirs := commitFileAs(t, dir, "theirs.txt", []byte("y\n"), "their commit", "someone-else@example.com", "someone else")

	email, err := RepoAuthorEmail(dir)
	if err != nil {
		t.Fatalf("RepoAuthorEmail: %v", err)
	}
	if email != "me@example.com" {
		t.Fatalf("RepoAuthorEmail = %q, want %q", email, "me@example.com")
	}

	filtered, err := CollectCommits(dir, email, "", 10)
	if err != nil {
		t.Fatalf("CollectCommits (filtered): %v", err)
	}
	if got := shas(filtered); len(got) != 1 || got[0] != mine {
		t.Fatalf("author-filtered CollectCommits = %v, want only %v", got, mine)
	}

	unfiltered, err := CollectCommits(dir, "", "", 10)
	if err != nil {
		t.Fatalf("CollectCommits (--all-authors): %v", err)
	}
	got := shas(unfiltered)
	if len(got) != 2 || got[0] != mine || got[1] != theirs {
		t.Fatalf("--all-authors CollectCommits = %v, want [%v %v]", got, mine, theirs)
	}
}

func TestRepoAuthorDefaultUnsetEmailFallsBackWithNotice(t *testing.T) {
	dir := newFixtureRepo(t)
	commitFileAs(t, dir, "f.txt", []byte("x\n"), "a commit", "someone@example.com", "someone")

	// No user.email configured anywhere (isolateGitEnv already keeps this
	// repo away from the host's real ~/.gitconfig).
	if _, err := RepoAuthorEmail(dir); err == nil {
		t.Fatalf("RepoAuthorEmail: expected an error when user.email is unset")
	}
}

// --- evidence formatting ---

func TestFormatGitEvidence(t *testing.T) {
	signals := []GitSignal{
		{Text: "old → new", Evidence: []string{"Commit: c", "File: f.go", "In: func Foo()", "-old\n+new"}},
	}
	block := FormatGitEvidence(signals)
	if !strings.HasPrefix(block, "- old → new\n  Commit: c\n  File: f.go\n  In: func Foo()\n  -old\n+new") {
		t.Fatalf("FormatGitEvidence = %q, want a bulleted signal with indented evidence", block)
	}
}

// --- LearnRepo cursor round-trip ---

func TestLearnRepoCursorRoundTrip(t *testing.T) {
	dir := newFixtureRepo(t)
	commitFile(t, dir, "a.txt", []byte("alpha\nbeta\n"), "add a")
	commitFile(t, dir, "a.txt", []byte("alpha\nBETA\n"), "tweak a")

	db := testDB(t)
	cfg := config.Default()

	reply, err := json.Marshal(struct {
		Preferences []DistilledStatement `json:"preferences"`
	}{Preferences: []DistilledStatement{
		{Statement: "Prefers uppercase constants.", Category: "Naming", Confidence: 0.8, Evidence: "beta -> BETA"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cg := &countingGateway{}
	server := newCountingSuccessServer(t, cg, string(reply))
	defer server.Close()
	gw := gatewayFor(server)

	n1, summary1, err := LearnRepo(context.Background(), db, cfg, gw, dir, "test@example.com", 200, 50)
	if err != nil {
		t.Fatalf("run 1: LearnRepo: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("run 1: signals = %d, want 1", n1)
	}
	if !summary1.Advanced || summary1.Applied != 1 {
		t.Fatalf("run 1: summary = %+v, want Advanced=true Applied=1", summary1)
	}
	if cg.count() != 1 {
		t.Fatalf("run 1: gateway calls = %d, want 1", cg.count())
	}

	n2, summary2, err := LearnRepo(context.Background(), db, cfg, gw, dir, "test@example.com", 200, 50)
	if err != nil {
		t.Fatalf("run 2: LearnRepo: %v", err)
	}
	if n2 != 0 || !summary2.Skipped {
		t.Fatalf("run 2: n=%d summary=%+v, want n=0 Skipped=true (cursor already covers every commit)", n2, summary2)
	}
	if cg.count() != 1 {
		t.Fatalf("run 2: gateway calls = %d, want still 1 (range form makes no new call)", cg.count())
	}
}

func TestRepoSourceClassification(t *testing.T) {
	local := t.TempDir()
	cases := map[string]struct {
		url    string
		remote bool
	}{
		local:                      {"", false},
		".":                        {"", false},
		"thinhkhuat/palate":        {"https://github.com/thinhkhuat/palate.git", true},
		"owner/repo.js":            {"https://github.com/owner/repo.js.git", true},
		"https://github.com/o/r":   {"https://github.com/o/r", true},
		"git@github.com:o/r.git":   {"git@github.com:o/r.git", true},
		"ssh://git@host/o/r":       {"ssh://git@host/o/r", true},
		"/definitely/not/here/o/r": {"", false},
		"three/part/name":          {"", false},
		"":                         {"", false},
	}
	for arg, want := range cases {
		url, remote := RepoSource(arg)
		if url != want.url || remote != want.remote {
			t.Fatalf("RepoSource(%q) = (%q, %v), want (%q, %v)", arg, url, remote, want.url, want.remote)
		}
	}
}

// CloneSource on a local bare repo: full history arrives, the temp dir is
// removed by cleanup, and a bad URL fails with git's own message.
func TestCloneSourceRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	src := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-q", "-am", "second")

	dir, cleanup, err := CloneSource(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	commits, err := CollectCommits(dir, "", "", 10)
	if err != nil || len(commits) != 2 {
		t.Fatalf("clone must carry the full history: %d commits, err %v", len(commits), err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the clone dir")
	}
	if _, _, err := CloneSource(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing source must fail")
	}
}
