package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/consolidate"
	"github.com/thinhkhuat/palate/internal/distill"
	"github.com/thinhkhuat/palate/internal/storage"
)

// dreamSetup builds a temp project whose gateway is a stub replying with the
// given actions, and which is due for a pass after one event. It returns the
// stub's call counter and URL.
func dreamSetup(t *testing.T, actions []map[string]interface{}) (calls *atomic.Int32, url string) {
	t.Helper()
	setupTempProject(t)
	calls = &atomic.Int32{}
	inner := actionsServer(t, actions)
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(counting.Close)
	url = counting.URL + "/v1/chat/completions"
	t.Setenv("PALATE_LLM_ENDPOINT", url)
	t.Setenv("PALATE_LLM_MODEL", "fake")
	t.Setenv("PALATE_CONSOLIDATE_EVERY", "1")
	return calls, url
}

// seedDistilled inserts n distilled one-session rows d00… (candidates, created
// now) and closes the store, so a following runCLI opens it fresh.
func seedDistilled(t *testing.T, n int) []string {
	t.Helper()
	db := openDB(loadConfig())
	ids := make([]string, n)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range ids {
		ids[i] = fmt.Sprintf("d%02d", i)
		_, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, reinforcement_count, trust_tier, veracity)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ids[i], "Testing", fmt.Sprintf("distilled candidate %02d", i), 0.5, 0.5, "llm_distilled", `["llm_distilled"]`, now, now, now, 0, "DISTILLED", "llm_distilled")
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.AppendEvent(db, "create", fmt.Sprintf(`{"id":%q,"c":0.5,"signal":"llm_distilled","session":"sess-%s"}`, ids[i], ids[i])); err != nil {
			t.Fatal(err)
		}
	}
	storage.Close()
	return ids
}

func readLog(t *testing.T) []consolidate.Attempt {
	t.Helper()
	got, err := consolidate.ReadAttempts(loadConfig())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func outcomes(as []consolidate.Attempt) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Outcome)
	}
	return out
}

func countRows(t *testing.T, q string, args ...interface{}) int {
	t.Helper()
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAttemptRecorded(t *testing.T) {
	dreamSetup(t, nil)

	// Seed the notice cursor before anything happens, as a first session would.
	cfg := loadConfig()
	db := openDB(cfg)
	if n, _ := learnedSinceNotice(db, cfg, "since your last session"); n != "" {
		t.Fatalf("the first notice only seeds the cursor, got %q", n)
	}
	storage.Close()

	ids := seedDistilled(t, 10)
	// 1. applied: the stub rescores one row.
	srv := actionsServer(t, []map[string]interface{}{{"type": "rescore", "id": ids[0], "confidence": 0.7, "reason": "reinforced"}})
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	out, err := runCLI(t, "consolidate")
	if err != nil || !strings.Contains(out, "consolidated 10 rows: 1 actions (1 applied, 0 proposed, 0 skipped) mode=auto") {
		t.Fatalf("applied pass: err=%v out=%q", err, out)
	}
	// 2. nothing-to-do: no candidates, profile unchanged, no gateway call.
	if out, err = runCLI(t, "consolidate"); err != nil || !strings.Contains(out, "consolidated 0 rows: 0 actions") {
		t.Fatalf("second pass: err=%v out=%q", err, out)
	}
	// 3. error: a dead gateway, forced to send everything with --rebuild.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/v1/chat/completions"
	dead.Close()
	t.Setenv("PALATE_LLM_ENDPOINT", deadURL)
	if _, err = runCLI(t, "consolidate", "--rebuild"); err == nil {
		t.Fatal("a dead gateway must fail the pass")
	}
	// 4. skipped-lock: a learner holds the lock.
	release, ok := distill.AcquireLearnLock(learnLockPath(loadConfig()))
	if !ok {
		t.Fatal("could not take the lock")
	}
	if _, err = runCLI(t, "consolidate"); err != nil {
		t.Fatalf("a busy lock is a recorded skip, not an error: %v", err)
	}
	release()

	log := readLog(t)
	if got := strings.Join(outcomes(log), ","); got != "applied,nothing-to-do,error,skipped-lock" {
		t.Fatalf("attempt log outcomes = %s", got)
	}
	if a := log[0]; a.Trigger != "manual" || a.Batches != 1 || a.Actions != 1 || a.EventID == 0 || a.At == "" {
		t.Fatalf("applied entry: %+v", a)
	}
	if a := log[1]; a.Batches != 0 || a.Actions != 0 || a.Error != "" {
		t.Fatalf("nothing-to-do entry: %+v", a)
	}
	if a := log[2]; !strings.Contains(a.Error, "<endpoint>") || strings.Contains(a.Error, deadURL) {
		t.Fatalf("the error entry must carry the failure with the endpoint redacted: %q", a.Error)
	}
	if a := log[3]; a.Trigger != "manual" || a.Error != "" || a.EventID <= log[2].EventID {
		t.Fatalf("skipped-lock entry: %+v", a)
	}

	// The next notice reports each failure once, then goes silent.
	cfg = loadConfig()
	db = openDB(cfg)
	defer storage.Close()
	notice, _ := learnedSinceNotice(db, cfg, "since your last session")
	if strings.Count(notice, "palate: profile clean-up failed at") != 1 ||
		!strings.Contains(notice, "— retrying next session; run 'palate consolidate' to retry now.") ||
		!strings.Contains(notice, "<endpoint>") || strings.Contains(notice, deadURL) {
		t.Fatalf("notice must show the redacted failure once:\n%s", notice)
	}
	if strings.Count(notice, "palate: profile clean-up skipped at") != 1 || !strings.Contains(notice, "another learn pass held the lock") {
		t.Fatalf("notice must show the skipped lock once:\n%s", notice)
	}
	if again, _ := learnedSinceNotice(db, cfg, "since your last session"); again != "" {
		t.Fatalf("the notice must go silent after reporting, got %q", again)
	}
}

func TestLockBusyIsRecorded(t *testing.T) {
	calls, _ := dreamSetup(t, []map[string]interface{}{{"type": "rescore", "id": "d00", "confidence": 0.7, "reason": "reinforced"}})
	seedDistilled(t, 10)
	cfg := loadConfig()
	lockPath := learnLockPath(cfg)

	release, ok := distill.AcquireLearnLock(lockPath)
	if !ok {
		t.Fatal("could not take the lock")
	}
	if _, err := runCLI(t, "consolidate"); err != nil {
		t.Fatal(err)
	}
	log := readLog(t)
	if len(log) != 1 || log[0].Outcome != "skipped-lock" || log[0].Trigger != "manual" {
		t.Fatalf("a busy lock must leave one skipped-lock line, got %+v", log)
	}
	if calls.Load() != 0 {
		t.Fatalf("a busy lock must short-circuit before any gateway call, got %d", calls.Load())
	}
	if n := countRows(t, `SELECT count(*) FROM consolidations`); n != 0 {
		t.Fatalf("a skipped attempt must write no consolidations row, got %d", n)
	}
	db := openDB(cfg)
	if _, _, due := consolidate.Due(db, cfg); !due {
		t.Fatal("Due must stay true after a skipped attempt")
	}
	storage.Close()

	// TouchLock keeps a long pass from being judged stale by a contender.
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := distill.TouchLock(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, got := distill.AcquireLearnLock(lockPath); got {
		t.Fatal("a touched lock must not be taken over as stale")
	}
	release()

	if out, err := runCLI(t, "consolidate"); err != nil || !strings.Contains(out, "(1 applied") {
		t.Fatalf("free lock: err=%v out=%q", err, out)
	}
	log = readLog(t)
	if len(log) != 2 || log[1].Outcome != "applied" || calls.Load() != 1 {
		t.Fatalf("free lock must run the pass once: log=%+v calls=%d", log, calls.Load())
	}
	if n := countRows(t, `SELECT count(*) FROM consolidations`); n != 1 {
		t.Fatalf("consolidations rows = %d, want 1", n)
	}
}

func TestLearnThenConsolidateLockBusyIsRecorded(t *testing.T) {
	calls, _ := dreamSetup(t, nil)
	cfg := loadConfig()
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	release, ok := distill.AcquireLearnLock(learnLockPath(cfg))
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer release()

	if _, err := runCLI(t, "learn", "session", transcript, "--harness", "claude_code", "--then-consolidate"); err != nil {
		t.Fatal(err)
	}
	log := readLog(t)
	if len(log) != 1 || log[0].Outcome != "skipped-lock" || log[0].Trigger != "session-start" {
		t.Fatalf("want one skipped-lock line from session-start, got %+v", log)
	}
	if n := countRows(t, `SELECT drops FROM capture_counters WHERE signal_type='learn_skipped_lock'`); n != 1 {
		t.Fatalf("learn_skipped_lock = %d, want 1", n)
	}
	// A plain learner (the Stop hook's child) skips visibly too, without a log line.
	if _, err := runCLI(t, "learn", "session", transcript, "--harness", "claude_code"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, `SELECT drops FROM capture_counters WHERE signal_type='learn_skipped_lock'`); n != 2 {
		t.Fatalf("learn_skipped_lock = %d, want 2", n)
	}
	if got := readLog(t); len(got) != 1 || calls.Load() != 0 {
		t.Fatalf("a plain skip adds no log line and calls no gateway: log=%d calls=%d", len(got), calls.Load())
	}
}

func TestLearnErrorStillConsolidates(t *testing.T) {
	calls, _ := dreamSetup(t, []map[string]interface{}{{"type": "rescore", "id": "d00", "confidence": 0.7, "reason": "reinforced"}})
	seedDistilled(t, 10)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")

	_, err := runCLI(t, "learn", "session", missing, "--harness", "claude_code", "--then-consolidate")
	if err == nil || !strings.Contains(err.Error(), "read messages") {
		t.Fatalf("the learn error must still be returned, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("the due pass must still run after a learn error: gateway calls = %d", calls.Load())
	}
	log := readLog(t)
	if len(log) != 1 || log[0].Outcome != "applied" || log[0].Trigger != "session-start" || !strings.HasPrefix(log[0].Error, "learn: read messages") {
		t.Fatalf("the attempt must carry the learn error: %+v", log)
	}
	if n := countRows(t, `SELECT count(*) FROM consolidations`); n != 1 {
		t.Fatalf("the pass must have written its consolidations row, got %d", n)
	}
}

func TestDreamAlias(t *testing.T) {
	dreamSetup(t, []map[string]interface{}{{"type": "rescore", "id": "d00", "confidence": 0.7, "reason": "reinforced"}})
	for _, name := range []string{"dream", "consolidate"} {
		out, err := runCLI(t, name, "--help")
		if err != nil || strings.Count(out, "--rebuild") != 1 {
			t.Fatalf("%s --help must list --rebuild once: err=%v\n%s", name, err, out)
		}
	}
	seedDistilled(t, 10)
	out, err := runCLI(t, "dream")
	if err != nil || !strings.Contains(out, "consolidated 10 rows: 1 actions (1 applied, 0 proposed, 0 skipped) mode=auto") {
		t.Fatalf("dream must run the same pass as consolidate: err=%v out=%q", err, out)
	}
	if log := readLog(t); len(log) != 1 || log[0].Outcome != "applied" || log[0].Trigger != "manual" {
		t.Fatalf("dream must record through the shared runner: %+v", log)
	}
	if out, err = runCLI(t, "dream", "--rebuild"); err != nil || !strings.Contains(out, "consolidated 10 rows:") {
		t.Fatalf("dream --rebuild must send every live row: err=%v out=%q", err, out)
	}
}

// A failure line must reach the notice even when no ledger event of the usual
// kinds happened since the last one.
func TestNoticeFailureWithoutLedgerChange(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	seedNoticeCursor(db, cfg)
	recordSkippedLock(cfg, db, "session-start")
	notice, learned := learnedSinceNotice(db, cfg, "since your last session")
	if !strings.HasPrefix(notice, "palate: profile clean-up skipped at ") || learned != "" || strings.Contains(notice, "profile +") {
		t.Fatalf("a skip alone must produce just the failure line, got %q / %q", notice, learned)
	}
}

// A long learn step can leave the lock older than the staleness bound before
// the pass's first gateway call; the pass must refresh the lock before that
// call, not only after the first batch, or a contender takes it over as stale.
func TestLockRefreshedBeforeFirstBatch(t *testing.T) {
	setupTempProject(t)
	lockPath := learnLockPath(loadConfig())
	var calls atomic.Int32
	var ageMS atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			if info, err := os.Stat(lockPath); err == nil {
				ageMS.Store(time.Since(info.ModTime()).Milliseconds())
			} else {
				ageMS.Store(-1)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"actions\":[]}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")
	t.Setenv("PALATE_LLM_MODEL", "fake")
	cfg := loadConfig()
	seedDistilled(t, 3)

	db := openDB(cfg)
	defer storage.Close()
	release, ok := distill.AcquireLearnLock(lockPath)
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer release()
	stale := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	if _, err := runConsolidatePass(context.Background(), cfg, db, "session-start", consolidate.RunOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("gateway calls = %d, want 1", calls.Load())
	}
	if got := time.Duration(ageMS.Load()) * time.Millisecond; got < 0 || got > time.Minute {
		t.Fatalf("lock age at the first gateway call = %v, want under 1m (it was set to 11m before the pass)", got)
	}
}

// Round 2: notice and attempt-log races.

// An attempt stamped past the newest event the notice has read (written
// between the notice reading the head and reading the log) waits for the next
// notice and is then reported exactly once.
func TestNoticeAttemptPastLastWaitsOneNotice(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	seedNoticeCursor(db, cfg)
	bump := func() {
		if err := storage.AppendEvent(db, "noop_test", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	bump()
	head := consolidate.MaxEventID(db)
	if err := consolidate.RecordAttempt(cfg, consolidate.Attempt{Trigger: "manual", Outcome: "error", Error: "boom", EventID: head + 1}); err != nil {
		t.Fatal(err)
	}
	if n1, _ := learnedSinceNotice(db, cfg, "since your last session"); strings.Contains(n1, "clean-up failed") {
		t.Fatalf("an attempt past the event head was reported early: %q", n1)
	}
	bump()
	n2, _ := learnedSinceNotice(db, cfg, "since your last session")
	if strings.Count(n2, "palate: profile clean-up failed") != 1 {
		t.Fatalf("the next notice must report it once: %q", n2)
	}
	bump()
	if n3, _ := learnedSinceNotice(db, cfg, "since your last session"); strings.Contains(n3, "clean-up failed") {
		t.Fatalf("it must not be reported a third time: %q", n3)
	}
}

// Recording an attempt (event append plus log line) is atomic with respect to
// a notice, which holds notice.lock while it reads the cursor and the log.
func TestAttemptRecordWaitsForNoticeLock(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	release, ok := distill.AcquireLearnLock(cfg.StatePath("notice.lock"))
	if !ok {
		t.Fatal("could not take the notice lock")
	}
	done := make(chan struct{})
	go func() {
		recordSkippedLock(cfg, db, "session-start")
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	if got, _ := consolidate.ReadAttempts(cfg); len(got) != 0 {
		t.Fatalf("the attempt was logged while a notice held its lock: %+v", got)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recording did not finish after the notice lock was released")
	}
	if got, _ := consolidate.ReadAttempts(cfg); len(got) != 1 || got[0].Outcome != "skipped-lock" {
		t.Fatalf("attempt log = %+v, want one skipped-lock line", got)
	}
}

// When the consolidate_attempt event cannot be appended, the attempt must
// still surface in a later notice instead of sitting at or below the cursor.
func TestFailedAttemptEventStillReported(t *testing.T) {
	setupTempProject(t)
	cfg := loadConfig()
	db := openDB(cfg)
	defer storage.Close()
	seedNoticeCursor(db, cfg)
	if _, err := db.Exec(`CREATE TRIGGER fail_attempt BEFORE INSERT ON events WHEN NEW.event_type='consolidate_attempt' BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	recordSkippedLock(cfg, db, "session-start")
	if _, err := db.Exec(`DROP TRIGGER fail_attempt`); err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEvent(db, "noop_test", `{}`); err != nil {
		t.Fatal(err)
	}
	n1, _ := learnedSinceNotice(db, cfg, "since your last session")
	if strings.Count(n1, "palate: profile clean-up skipped") != 1 {
		t.Fatalf("the failure must be reported once: %q", n1)
	}
	if err := storage.AppendEvent(db, "noop_test", `{}`); err != nil {
		t.Fatal(err)
	}
	if n2, _ := learnedSinceNotice(db, cfg, "since your last session"); strings.Contains(n2, "clean-up skipped") {
		t.Fatalf("it must not be reported twice: %q", n2)
	}
}
