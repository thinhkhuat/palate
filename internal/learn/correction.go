package learn

import (
	"database/sql"
	"encoding/json"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

// Correction is the four-field correction schema exposed by `palate correct`
// and the palate_correct MCP tool. Fix is the statement that gets learned;
// the other fields are kept on the events log so the contract is not a lie.
type Correction struct {
	Tried          string `json:"tried"`
	WrongBecause   string `json:"wrong_because,omitempty"`
	Fix            string `json:"fix"`
	TriggerContext string `json:"trigger_context,omitempty"`
}

// Statement is what gets learned: the fix, falling back to what was tried.
func (c Correction) Statement() string {
	if c.Fix != "" {
		return c.Fix
	}
	return c.Tried
}

// Texts returns every field that gets persisted, so callers can run the
// safety scan over all of them, not only the learned statement.
func (c Correction) Texts() []string {
	return []string{c.Tried, c.WrongBecause, c.Fix, c.TriggerContext}
}

// RecordCorrection applies the explicit_correction signal and appends one
// "correct" event carrying all four fields.
func RecordCorrection(conn *sql.DB, cfg config.Config, c Correction) (*Preference, error) {
	stmt := c.Statement()
	p, err := ApplySignal(conn, cfg, Signal{Type: SignalExplicitCorrection, Statement: stmt, Category: InferCategoryFromStatement(stmt), TriggerContext: c.TriggerContext}, nil)
	if err != nil {
		return nil, err
	}
	c.Fix = stmt
	payload, _ := json.Marshal(c)
	_ = storage.AppendEvent(conn, "correct", string(payload))
	return p, nil
}
