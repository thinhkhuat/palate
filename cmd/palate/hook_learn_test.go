package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
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
	state := loadModelEdits(filepath.Join(".palate", "model_edits.json"))
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
	if err := os.MkdirAll(".palate", 0o700); err != nil {
		t.Fatalf("mkdir .palate: %v", err)
	}
	if err := os.WriteFile("watched.txt", []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write watched.txt: %v", err)
	}
	state := map[string]modelEdit{"watched.txt": {Hash: "deadbeef", Snapshot: "line one\n"}}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal model_edits: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".palate", "model_edits.json"), b, 0o600); err != nil {
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
	unconfiguredCfg := config.Config{DBPath: ".palate/palate.db"}
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
