package learn

import (
	"database/sql"
	"fmt"
	"time"
)

type Proposal struct {
	ID             string  `json:"id"`
	Content        string  `json:"content"`
	Category       string  `json:"category"`
	Source         string  `json:"source"`
	ProposedWeight float64 `json:"proposed_weight"`
	Status         string  `json:"status"`
	CreatedAt      string  `json:"created_at"`
}

func CreateProposal(conn *sql.DB, content, category string) (string, error) {
	id := newID()
	now := time.Now().UTC().Format(time.RFC3339)
	if category == "" {
		category = InferCategoryFromStatement(content)
	}
	_, err := conn.Exec(`INSERT INTO proposals(id, content, category, source, proposed_weight, status, created_at) VALUES(?,?,?,?,?,?,?)`,
		id, content, category, "llm", 0.4, "pending", now)
	if err != nil {
		return "", err
	}
	return id, nil
}

func ListProposals(conn *sql.DB) ([]Proposal, error) {
	rows, err := conn.Query(`SELECT id, content, category, source, proposed_weight, status, created_at FROM proposals ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var p Proposal
		if err := rows.Scan(&p.ID, &p.Content, &p.Category, &p.Source, &p.ProposedWeight, &p.Status, &p.CreatedAt); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func PromoteProposal(conn *sql.DB, proposalID, asSignal string, cfg interface {
	WeightFor(string) float64
}) (*Proposal, error) {
	var p Proposal
	err := conn.QueryRow(`SELECT id, content, category, source, proposed_weight, status, created_at FROM proposals WHERE id=?`, proposalID).Scan(
		&p.ID, &p.Content, &p.Category, &p.Source, &p.ProposedWeight, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("proposal not found: %w", err)
	}
	if p.Status != "pending" {
		return nil, fmt.Errorf("proposal already %s", p.Status)
	}
	w := 0.8
	if asSignal != "" {
		w = cfg.WeightFor(asSignal)
		if w == 0 {
			w = 0.8
		}
	}
	sig := Signal{Type: SignalType(asSignal), Statement: p.Content, Category: p.Category}
	if sig.Type == "" {
		sig.Type = SignalStatedPreference
	}
	_, err = conn.Exec(`UPDATE proposals SET status='promoted' WHERE id=?`, proposalID)
	if err != nil {
		return nil, err
	}
	_ = sig
	_ = w
	return &p, nil
}

func PromoteProposalWithApply(conn *sql.DB, proposalID, asSignal string, cfg interface {
	WeightFor(string) float64
}, apply func(Signal) error) (*Proposal, error) {
	p, err := PromoteProposal(conn, proposalID, asSignal, cfg)
	if err != nil {
		return nil, err
	}
	sig := Signal{Type: SignalType(asSignal), Statement: p.Content, Category: p.Category}
	if sig.Type == "" {
		sig.Type = SignalStatedPreference
	}
	if err := apply(sig); err != nil {
		return nil, err
	}
	return p, nil
}
