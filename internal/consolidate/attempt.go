package consolidate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
)

// attemptLogRotateAt/attemptLogKeep: state/consolidate.log is rotated once it
// passes this many lines, keeping the newest attemptLogKeep (req 8).
const (
	attemptLogRotateAt = 2000
	attemptLogKeep     = 1000
)

// Attempt records one completed consolidate/dream pass attempt — applied,
// proposals, a no-op, a busy lock, or an error (req 8). state/consolidate.log
// is the only state file this subsystem writes: there is no separate status
// file and no "notified" flag.
type Attempt struct {
	At      string `json:"at"`
	Trigger string `json:"trigger"`
	Outcome string `json:"outcome"` // applied | proposals | nothing-to-do | skipped-lock | error
	Batches int    `json:"batches,omitempty"`
	// Actions is every action the model returned; Applied + Proposed + Skipped
	// == Actions, and SkipReasons splits Skipped by why.
	Actions     int            `json:"actions,omitempty"`
	Applied     int            `json:"applied,omitempty"`
	Proposed    int            `json:"proposed,omitempty"`
	Skipped     int            `json:"skipped,omitempty"`
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`
	// BatchMS is each batch's gateway wait in ms (retries included), in order;
	// DurationMS stays the whole attempt.
	BatchMS []int64 `json:"batch_ms,omitempty"`
	// ApplyErrors samples (capped) the actions that failed or were malformed.
	ApplyErrors []ApplyError `json:"apply_errors,omitempty"`
	Retired     int          `json:"retired,omitempty"`
	Error       string       `json:"error,omitempty"`
	DurationMS  int64        `json:"duration_ms,omitempty"`
	// EventID is MAX(events.id) at the time of the attempt — the caller (it
	// holds the connection) fills this in before calling RecordAttempt.
	EventID int64 `json:"event_id"`
}

// AttemptLogPath is state/consolidate.log.
func AttemptLogPath(cfg config.Config) string {
	return cfg.StatePath("consolidate.log")
}

// RecordAttempt appends one JSON line to the attempt log, redacting the
// configured gateway endpoint/key out of a.Error first (Security), and
// rotates the log once it passes attemptLogRotateAt lines.
func RecordAttempt(cfg config.Config, a Attempt) error {
	if a.At == "" {
		a.At = time.Now().UTC().Format(time.RFC3339)
	}
	a.Error = redactGatewaySecrets(cfg, a.Error)
	if len(a.ApplyErrors) > 0 {
		redacted := make([]ApplyError, len(a.ApplyErrors))
		for i, e := range a.ApplyErrors {
			e.Error = redactGatewaySecrets(cfg, e.Error)
			redacted[i] = e
		}
		a.ApplyErrors = redacted
	}
	path := AttemptLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return rotateAttemptLog(path)
}

// rotateAttemptLog keeps the log from growing forever: once it exceeds
// attemptLogRotateAt lines, only the newest attemptLogKeep survive.
func rotateAttemptLog(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= attemptLogRotateAt {
		return nil
	}
	kept := lines[len(lines)-attemptLogKeep:]
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// ReadAttempts parses every line in the attempt log, oldest first, silently
// skipping a malformed line. A missing log (never run yet) returns nil, nil.
func ReadAttempts(cfg config.Config) ([]Attempt, error) {
	data, err := os.ReadFile(AttemptLogPath(cfg))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil, nil
	}
	var out []Attempt
	for _, line := range strings.Split(trimmed, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var a Attempt
		if json.Unmarshal([]byte(line), &a) != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// redactGatewaySecrets replaces any substring equal to the configured
// gateway endpoint or API key with a placeholder before an error string is
// persisted (Security section) — the same values distill.Gateway reads from
// cfg.LLM.
func redactGatewaySecrets(cfg config.Config, s string) string {
	if s == "" {
		return s
	}
	if cfg.LLM.Endpoint != "" {
		s = strings.ReplaceAll(s, cfg.LLM.Endpoint, "<endpoint>")
	}
	if cfg.LLM.APIKey != "" {
		s = strings.ReplaceAll(s, cfg.LLM.APIKey, "<key>")
	}
	return s
}
