package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

// spawnCall records one spawnLearnSession invocation captured by the spy
// installed via installSpawnSpy.
type spawnCall struct {
	bin  string
	path string
}

// installSpawnSpy swaps the package-level spawnLearnSession seam for a spy
// that records calls instead of starting a real child process, restoring
// the original on test cleanup.
func installSpawnSpy(t *testing.T) *[]spawnCall {
	t.Helper()
	calls := &[]spawnCall{}
	orig := spawnLearnSession
	spawnLearnSession = func(bin, transcriptPath string) error {
		*calls = append(*calls, spawnCall{bin, transcriptPath})
		return nil
	}
	t.Cleanup(func() { spawnLearnSession = orig })
	return calls
}

func TestStopHookSpawnsDetached(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("PALATE_LLM_MODEL", "x")
	calls := installSpawnSpy(t)

	start := time.Now()
	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/whatever.jsonl"}`, "hook", "stop")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stop hook took %v, want well under 1s", elapsed)
	}

	if len(*calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d: %+v", len(*calls), *calls)
	}
	if (*calls)[0].path != "/tmp/whatever.jsonl" {
		t.Fatalf("spawn called with wrong transcript path: %+v", (*calls)[0])
	}
	if (*calls)[0].bin == "" {
		t.Fatal("spawn called with an empty binary path")
	}
}

func TestSessionStartSpawnsDetached(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("PALATE_LLM_MODEL", "x")
	calls := installSpawnSpy(t)

	start := time.Now()
	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/whatever.jsonl"}`, "hook", "session-start")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("session-start hook took %v, want well under 1s", elapsed)
	}

	if len(*calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d: %+v", len(*calls), *calls)
	}
	if (*calls)[0].path != "/tmp/whatever.jsonl" {
		t.Fatalf("spawn called with wrong transcript path: %+v", (*calls)[0])
	}
}

// TestHooksDoNotSpawnWhenUnconfigured is the regression guard: with no LLM
// configured, neither hook spawns. The verbatim user_diff learn path is
// retired (2026-09-15): a hand-edit is counted and cleared, never stored —
// the same edit is covered by the LLM distiller via the transcript. Stop
// still learns nothing (the drift is the assistant's own) and re-baselines.
func TestHooksDoNotSpawnWhenUnconfigured(t *testing.T) {
	setupTempProject(t)
	calls := installSpawnSpy(t)

	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/whatever.jsonl"}`, "hook", "session-start")
	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/whatever.jsonl"}`, "hook", "stop")
	if len(*calls) != 0 {
		t.Fatalf("expected no spawn calls with LLM unconfigured, got %+v", *calls)
	}

	writeStaleSnapshot(t)
	runHook(t, `{"session_id":"s1"}`, "hook", "stop")
	out, err := runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(out, "prefers:") {
		t.Fatalf("Stop must not learn a user_diff (the drift is the assistant's own), got: %s", out)
	}
	// Stop re-baselines: the recorded hash now matches the bytes on disk, and
	// the snapshot is the current content, so the same drift cannot be
	// credited to the user later.
	state := loadModelEdits(loadConfig().StatePath("model_edits.json"))
	disk, _ := os.ReadFile("watched.txt")
	if rec, ok := state["watched.txt"]; !ok || rec.Hash != fileHash(disk) || rec.Snapshot != string(disk) {
		t.Fatalf("Stop must re-baseline the snapshot to disk, got %+v", state)
	}

	writeStaleSnapshot(t)
	runHook(t, `{"prompt":"carry on"}`, "hook", "user-prompt")
	out, err = runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(out, "prefers:") {
		t.Fatalf("retired verbatim path must never store a prefers-code row, got: %s", out)
	}
	var drops int
	cfg := loadConfig()
	db := openDB(cfg)
	_ = db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='user_diff'`).Scan(&drops)
	storage.Close()
	if drops < 1 {
		t.Fatalf("the drifted hand-edit must be counted as a user_diff drop, drops=%d", drops)
	}
	if len(*calls) != 0 {
		t.Fatalf("expected no spawn calls after the regression check, got %+v", *calls)
	}
}

// writeStaleSnapshot plants a model_edits.json entry whose hash and snapshot
// predate the current bytes of watched.txt — the shape a hand-edit leaves.
func writeStaleSnapshot(t *testing.T) {
	t.Helper()
	cfg := loadConfig()
	if err := os.MkdirAll(cfg.StateDir(), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile("watched.txt", []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write watched.txt: %v", err)
	}
	state := map[string]modelEdit{"watched.txt": {Hash: "deadbeef", Snapshot: "line one\n"}}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal model_edits: %v", err)
	}
	if err := os.WriteFile(cfg.StatePath("model_edits.json"), b, 0o600); err != nil {
		t.Fatalf("write model_edits.json: %v", err)
	}
}

// TestUserPromptRegexOffWhenLLMConfigured: with an LLM configured, a prompt
// matching the stated-preference regex must not become a preference row —
// the same text is already sitting in the transcript the Stop hook distills.
func TestUserPromptRegexOffWhenLLMConfigured(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("PALATE_LLM_MODEL", "x")

	runHook(t, `{"prompt":"I prefer table-driven tests for Go packages"}`, "hook", "user-prompt")
	out, err := runCLI(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(out, "table-driven tests") {
		t.Fatalf("regex-extracted preference stored despite LLM being configured: %s", out)
	}
}

// TestApplyUserDiffSignalRetired: the verbatim learn path is retired — a changed
// file with a real diff produces zero preference rows and one
// BumpDrop("user_diff") whether or not an LLM is configured. The same edit is
// covered by the detached distiller via the transcript.
func TestApplyUserDiffSignalOffWhenLLMConfigured(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("PALATE_LLM_MODEL", "x")
	cfg := loadConfig()
	db := openDB(cfg)

	if err := os.WriteFile("watched.txt", []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write watched.txt: %v", err)
	}
	if !applyUserDiffSignal(db, cfg, "watched.txt", "deadbeef", "line one\n", "") {
		t.Fatal("expected applyUserDiffSignal to report handled when LLM is configured")
	}
	prefs, err := learn.ListPreferences(db, true)
	if err != nil {
		t.Fatalf("list preferences: %v", err)
	}
	if len(prefs) != 0 {
		t.Fatalf("expected zero preferences with LLM configured, got %d: %+v", len(prefs), prefs)
	}
	var drops int
	_ = db.QueryRow(`SELECT drops FROM capture_counters WHERE signal_type='user_diff'`).Scan(&drops)
	if drops != 1 {
		t.Fatalf("expected 1 user_diff drop, got %d", drops)
	}
	storage.Close()

	// Regression: with no LLM configured, the same shaped diff is likewise
	// counted and cleared — never stored. (Pre-2026-09-15 behaviour stored
	// one "prefers:" row here; that path poisoned TASTE.md with 53 verbatim
	// dumps and is retired.)
	setupTempProject(t)
	unconfiguredCfg := loadConfig()
	db2 := openDB(unconfiguredCfg)
	if err := os.WriteFile("watched.txt", []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write watched.txt: %v", err)
	}
	if !applyUserDiffSignal(db2, unconfiguredCfg, "watched.txt", "deadbeef", "line one\n", "") {
		t.Fatal("expected applyUserDiffSignal to report handled when unconfigured")
	}
	prefs2, err := learn.ListPreferences(db2, true)
	if err != nil {
		t.Fatalf("list preferences: %v", err)
	}
	if len(prefs2) != 0 {
		t.Fatalf("retired path must store zero preferences, got %d: %+v", len(prefs2), prefs2)
	}
}

// SessionStart tells the user what was learned since the previous session
// (Command Code's inline "taste learned" notices), and says nothing when
// nothing changed. The first run on a home only seeds the cursor.
func TestSessionStartNoticeReportsNewPreferences(t *testing.T) {
	setupTempProject(t)
	first := runHook(t, `{"session_id":"n1","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	if strings.Contains(first, "systemMessage") {
		t.Fatalf("first run must only seed the cursor, got %s", first)
	}
	if _, err := runCLI(t, "prefer", "Prefers notices about what was learned"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "prefer", "Prefers a second notice line"); err != nil {
		t.Fatal(err)
	}
	out := runHook(t, `{"session_id":"n2","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	var parsed struct {
		SystemMessage string `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("hook output not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(parsed.SystemMessage, "learned 2") || !strings.Contains(parsed.SystemMessage, "Prefers notices about what was learned") {
		t.Fatalf("notice missing the new preferences: %q", parsed.SystemMessage)
	}
}

func TestSessionStartNoticeSilentWhenNothingNew(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "Prefers silence when nothing changed"); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"q1","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	out := runHook(t, `{"session_id":"q2","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	if strings.Contains(out, "systemMessage") {
		t.Fatalf("no new events, yet a notice was emitted: %s", out)
	}
	if !strings.Contains(out, "additionalContext") {
		t.Fatalf("profile block still expected: %s", out)
	}
}

// A compaction restarts the same session, so it must not claim "since your
// last session"; the next real start still reports the delta.
func TestSessionStartNoticeSkipsCompaction(t *testing.T) {
	setupTempProject(t)
	runHook(t, `{"session_id":"c0","transcript_path":"/tmp/none.jsonl","source":"startup"}`, "hook", "session-start")
	if _, err := runCLI(t, "prefer", "Prefers the notice to wait past compaction"); err != nil {
		t.Fatal(err)
	}
	mid := runHook(t, `{"session_id":"c0","transcript_path":"/tmp/none.jsonl","source":"compact"}`, "hook", "session-start")
	if strings.Contains(mid, "systemMessage") {
		t.Fatalf("compaction must not emit the notice: %s", mid)
	}
	next := runHook(t, `{"session_id":"c1","transcript_path":"/tmp/none.jsonl","source":"startup"}`, "hook", "session-start")
	if !strings.Contains(next, "learned 1") {
		t.Fatalf("next real start must still report the delta: %s", next)
	}
}

// Consolidation housekeeping is reported as consolidated, never as "New:".
func TestSessionStartNoticeBucketsConsolidation(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "Prefers merged rows to be reported honestly"); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"m0","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	if err := storage.AppendEvent(db, "consolidate_rewrite", `{"id":"x","from":"a","to":"b","category":"","reason":"test"}`); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	out := runHook(t, `{"session_id":"m1","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	if !strings.Contains(out, "consolidated 1") || strings.Contains(out, "New:") {
		t.Fatalf("consolidation must be its own bucket without a New: quote: %s", out)
	}
}

// The next prompt after something was learned announces it (systemMessage)
// and hands the model the new lines (additionalContext) — Command Code's
// inline "taste learned" while you work.
func TestUserPromptNoticeReportsLearned(t *testing.T) {
	setupTempProject(t)
	first := runHook(t, `{"prompt":"hello"}`, "hook", "user-prompt")
	if strings.Contains(first, "systemMessage") {
		t.Fatalf("first run must only seed the cursor, got %s", first)
	}
	if _, err := runCLI(t, "prefer", "Prefers inline notices about freshly learned taste"); err != nil {
		t.Fatal(err)
	}
	out := runHook(t, `{"prompt":"carry on with the refactor"}`, "hook", "user-prompt")
	var parsed struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("hook output not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(parsed.SystemMessage, "since your last prompt — learned 1") {
		t.Fatalf("notice missing: %q", parsed.SystemMessage)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "<palate-learned>") || !strings.Contains(ctx, "Prefers inline notices about freshly learned taste") || !strings.Contains(ctx, "Confidence: ") {
		t.Fatalf("learned block missing or malformed: %q", ctx)
	}
}

func TestUserPromptNoticeSilentWhenNothingNew(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "Prefers quiet prompts when nothing changed"); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"prompt":"seed"}`, "hook", "user-prompt")
	out := runHook(t, `{"prompt":"nothing new here"}`, "hook", "user-prompt")
	if strings.Contains(out, "systemMessage") || strings.Contains(out, "palate-learned") {
		t.Fatalf("no new events, yet a notice was emitted: %s", out)
	}
}

// distill.mode off and distill.skip_dirs keep both hooks from spawning the
// learner; an unrelated cwd still spawns.
func TestHooksHonourLearningOffAndSkipDirs(t *testing.T) {
	setupTempProject(t)
	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("PALATE_LLM_MODEL", "x")
	calls := installSpawnSpy(t)

	t.Setenv("PALATE_DISTILL_MODE", "off")
	runHook(t, `{"session_id":"o1","transcript_path":"/tmp/t.jsonl","cwd":"/work/a"}`, "hook", "stop")
	runHook(t, `{"session_id":"o1","transcript_path":"/tmp/t.jsonl","cwd":"/work/a"}`, "hook", "session-start")
	if len(*calls) != 0 {
		t.Fatalf("mode off must not spawn, got %+v", *calls)
	}

	t.Setenv("PALATE_DISTILL_MODE", "")
	if err := os.WriteFile("config.yaml", []byte("db_path: .palate/palate.db\ndistill:\n  skip_dirs: [\"/work/clones\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"o2","transcript_path":"/tmp/t.jsonl","cwd":"/work/clones/foreign"}`, "hook", "stop")
	if len(*calls) != 0 {
		t.Fatalf("a skipped cwd must not spawn, got %+v", *calls)
	}
	runHook(t, `{"session_id":"o3","transcript_path":"/tmp/t.jsonl","cwd":"/work/mine"}`, "hook", "stop")
	if len(*calls) != 1 {
		t.Fatalf("an unrelated cwd must spawn once, got %+v", *calls)
	}
}

func TestDoctorLearningLine(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PALATE_DISTILL_MODE", "review")
	out, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "learning: review") {
		t.Fatalf("doctor must print the learning mode:\n%s", out)
	}
}

// init seeds the notice cursor, so a preference stated before the first hook
// ever runs is announced by that first hook instead of swallowed.
func TestInitSeedsNoticeCursor(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "prefer", "Prefers the very first hook to announce this"); err != nil {
		t.Fatal(err)
	}
	out := runHook(t, `{"prompt":"first ever prompt"}`, "hook", "user-prompt")
	if !strings.Contains(out, "since your last prompt — learned 1") || !strings.Contains(out, "Prefers the very first hook to announce this") {
		t.Fatalf("first hook after init must announce the backlog: %s", out)
	}
}

// A dead gateway during learn repo is a visible, non-zero failure — never
// "0 preferences" with exit 0.
func TestLearnRepoDeadGatewayFails(t *testing.T) {
	setupTempProject(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package main\n\nfunc run(n int) int {\n\treturn n\n}\n")
	git("add", "main.go")
	git("commit", "-q", "-m", "first")
	// A replaced line (removed + added in one hunk) is what ExtractSignals
	// turns into a correction signal.
	write("package main\n\nfunc run(n int) int {\n\treturn max(n, 0)\n}\n")
	git("commit", "-q", "-am", "guard negative input")

	t.Setenv("PALATE_LLM_ENDPOINT", "http://127.0.0.1:1/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "x")
	_, err := runCLI(t, "learn", "repo", "--all-authors", repo)
	if err == nil || !strings.Contains(err.Error(), "not advanced") {
		t.Fatalf("dead gateway must fail visibly, got err=%v", err)
	}
}

// The wiring main() performs: with the seam set, a distilled paraphrase of an
// existing row is judged (heuristic here: exact match → same) and reinforces
// it instead of landing as a second row.
func TestWireDistillJudgeRoutesDistilledWrites(t *testing.T) {
	setupTempProject(t)
	wireDistillJudge()
	t.Cleanup(func() { distill.Judge = nil })
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	seed, err := learn.ApplySignal(db, cfg, learn.Signal{Type: learn.SignalExplicitPrefer, Statement: "Prefers table-driven tests.", Category: "Testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, dropped := distill.ApplyDistilled(db, cfg, []distill.DistilledStatement{{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.7, Evidence: "again"}})
	if applied != 1 || dropped != 0 {
		t.Fatalf("applied=%d dropped=%d", applied, dropped)
	}
	var rows int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&rows)
	var c float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE id=?`, seed.ID).Scan(&c)
	if rows != 1 || c <= seed.Confidence {
		t.Fatalf("judged same must reinforce the one row: rows=%d c %.2f→%.2f", rows, seed.Confidence, c)
	}
	var by int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='reinforce' AND payload LIKE '%"by":"heuristic"%'`).Scan(&by)
	if by != 1 {
		t.Fatal("the reinforce event must name the judge")
	}
}
