package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

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
// configured, neither hook spawns, and the user_diff state walk still learns
// a hand-edit — at UserPromptSubmit, never at Stop.
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
	if !strings.Contains(out, "prefers:") {
		t.Fatalf("UserPromptSubmit must learn the user_diff left since Stop, got: %s", out)
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

// TestApplyUserDiffSignalOffWhenLLMConfigured: with an LLM configured, a
// changed file with a real diff produces zero preference rows and one
// BumpDrop("user_diff"); with it unset, today's behavior (a preference row
// appears) is unchanged.
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

	// Regression: with no LLM configured, the same shaped diff still stores
	// a preference exactly as it did before this phase.
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
	if len(prefs2) != 1 {
		t.Fatalf("expected exactly 1 preference with LLM unconfigured, got %d: %+v", len(prefs2), prefs2)
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
