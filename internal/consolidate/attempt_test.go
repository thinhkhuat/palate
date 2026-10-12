package consolidate

import (
	"os"
	"strings"
	"testing"
)

func TestAttemptRedactsGatewaySecrets(t *testing.T) {
	_, cfg := testStore(t)
	cfg.LLM.Endpoint, cfg.LLM.APIKey = "https://gw.example.test/v1/chat/completions", "sk-very-secret"
	err := RecordAttempt(cfg, Attempt{Trigger: "manual", Outcome: "error", Error: `Post "https://gw.example.test/v1/chat/completions": bad key sk-very-secret`, EventID: 7})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(AttemptLogPath(cfg))
	if strings.Contains(string(raw), "gw.example.test") || strings.Contains(string(raw), "sk-very-secret") {
		t.Fatalf("the log must not carry the endpoint or key: %s", raw)
	}
	got, err := ReadAttempts(cfg)
	if err != nil || len(got) != 1 {
		t.Fatalf("read back: %v %+v", err, got)
	}
	if want := `Post "<endpoint>": bad key <key>`; got[0].Error != want || got[0].EventID != 7 || got[0].Outcome != "error" {
		t.Fatalf("got %+v, want error %q with event_id 7", got[0], want)
	}
}

func TestAttemptLogRotates(t *testing.T) {
	_, cfg := testStore(t)
	for i := 1; i <= attemptLogRotateAt+1; i++ {
		if err := RecordAttempt(cfg, Attempt{Trigger: "manual", Outcome: "nothing-to-do", EventID: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadAttempts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != attemptLogKeep || got[0].EventID != int64(attemptLogRotateAt+1-attemptLogKeep+1) || got[len(got)-1].EventID != int64(attemptLogRotateAt+1) {
		t.Fatalf("kept %d lines from event %d to %d, want the newest %d", len(got), got[0].EventID, got[len(got)-1].EventID, attemptLogKeep)
	}
}
