package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/consolidate"
	"github.com/thinhkhuat/palate/internal/storage"
)

// The attempt record carries each batch's duration beside the pass total, and
// its action counts add up: applied + proposed + skipped == actions.
func TestAttemptLogsPerBatchDurations(t *testing.T) {
	dreamSetup(t, nil)
	seedDistilled(t, 45) // 40 + 5 candidates: two batches
	var call atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := call.Add(1)
		delay, actions := 250*time.Millisecond, []map[string]interface{}{{"type": "retire", "id": "d00", "reason": "one-off"}, {"type": "retire", "id": "nope", "reason": "one-off"}}
		if n == 2 {
			delay, actions = 30*time.Millisecond, []map[string]interface{}{{"type": "rescore", "id": "d41", "confidence": 0.7, "reason": "reinforced"}}
		}
		time.Sleep(delay)
		body, _ := json.Marshal(map[string]interface{}{"actions": actions})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": string(body)}}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PALATE_LLM_ENDPOINT", srv.URL+"/v1/chat/completions")

	if _, err := runCLI(t, "consolidate", "--rebuild"); err != nil {
		t.Fatal(err)
	}
	log := readLog(t)
	if len(log) != 1 {
		t.Fatalf("attempt log = %+v, want one entry", log)
	}
	a := log[0]
	if a.Batches != 2 || len(a.BatchMS) != 2 {
		t.Fatalf("batches=%d batch_ms=%v, want 2 batches and 2 durations", a.Batches, a.BatchMS)
	}
	if a.BatchMS[0] < 250 || a.BatchMS[1] < 30 || a.BatchMS[0] <= a.BatchMS[1] {
		t.Fatalf("batch_ms = %v, want [>=250, >=30] with the slow batch first", a.BatchMS)
	}
	if sum := a.BatchMS[0] + a.BatchMS[1]; a.DurationMS < sum {
		t.Fatalf("duration_ms = %d is less than the batches' %d", a.DurationMS, sum)
	}
	if a.Actions != 3 || a.Applied != 2 || a.Skipped != 1 || a.Applied+a.Proposed+a.Skipped != a.Actions {
		t.Fatalf("record %+v, want 3 actions = 2 applied + 0 proposed + 1 skipped", a)
	}
	if a.SkipReasons["unknown-id"] != 1 {
		t.Fatalf("skip_reasons = %v, want one unknown-id", a.SkipReasons)
	}
}

// A hold whose header line was removed is not "an out-of-date copy", and the
// notice says what the accept needs: --yes for restored lines and for more
// than the line cap.
func TestNoticeNamesYesAndHeaderRemoval(t *testing.T) {
	setupTempProject(t)
	if _, err := runCLI(t, "prefer", "Prefers a seed row to render the file"); err != nil {
		t.Fatal(err)
	}
	runHook(t, `{"session_id":"s1","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	cfg := loadConfig()
	b, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	noHeader := strings.SplitN(string(b), "\n", 2)[1]
	if err := os.WriteFile(cfg.TastePath(), []byte(noHeader+"\n## Testing\n\n- Prefers a hand-added row while held. Confidence: 0.60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runHook(t, `{"session_id":"s2","transcript_path":"/tmp/none.jsonl"}`, "hook", "session-start")
	if strings.Contains(out, "out-of-date") {
		t.Fatalf("a header-removed hold is not an out-of-date copy: %s", out)
	}
	for _, want := range []string{"header line was removed", "--accept-edits", "--yes", "10"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the notice must mention %q: %s", want, out)
		}
	}
}

// An action that fails inside applyAction is never silent: it prints one
// stderr line (type, id, error; no statement text) and lands, redacted and
// capped, in the attempt record's apply_errors beside the apply-error count.
func TestApplyErrorIsPrintedAndLogged(t *testing.T) {
	dreamSetup(t, []map[string]interface{}{{"type": "retire", "id": "d00", "reason": "one-off"}})
	seedDistilled(t, 3)
	db := openDB(loadConfig())
	_, err := db.Exec(`CREATE TRIGGER fail_d00 BEFORE UPDATE ON preferences WHEN OLD.id='d00' BEGIN SELECT RAISE(ABORT, 'disk on fire'); END`)
	storage.Close()
	if err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runCLIStderr(t, "consolidate", "--rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if want := "palate: consolidate: retire of d00 failed: "; !strings.Contains(stderr, want) || !strings.Contains(stderr, "disk on fire") {
		t.Fatalf("stderr = %q, want a %q line carrying the cause", stderr, want)
	}
	raw, rerr := os.ReadFile(consolidate.AttemptLogPath(loadConfig()))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var rec struct {
		SkipReasons map[string]int `json:"skip_reasons"`
		ApplyErrors []struct {
			Type, ID, Error string
		} `json:"apply_errors"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec); err != nil {
		t.Fatalf("attempt log %q: %v", raw, err)
	}
	if rec.SkipReasons["apply-error"] != 1 || len(rec.ApplyErrors) != 1 {
		t.Fatalf("record %s, want one apply-error and one apply_errors entry", raw)
	}
	if e := rec.ApplyErrors[0]; e.Type != "retire" || e.ID != "d00" || !strings.Contains(e.Error, "disk on fire") {
		t.Fatalf("apply_errors[0] = %+v, want retire/d00 with the cause", e)
	}
}
