package verify

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
)

type Report struct {
	Version       string            `json:"version"`
	ScenariosHash string            `json:"scenarios_hash"`
	Judge         string            `json:"judge"`
	Pure          bool              `json:"pure"`
	CaptureRates  map[string]float64 `json:"capture_rates"`
	Caveats       string            `json:"caveats"`
}

func ScenariosHash() string {
	h := sha256.New()
	for _, s := range ScenariosV1 {
		io.WriteString(h, s.ID+s.Prompt+s.Expected)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func RunCapture(conn *sql.DB, pure bool) (Report, error) {
	rows, err := conn.Query(`SELECT signal_type, hits, drops FROM capture_counters`)
	rates := map[string]float64{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t string
			var hits, drops int
			if err := rows.Scan(&t, &hits, &drops); err != nil {
				continue
			}
			total := float64(hits + drops)
			if total == 0 {
				rates[t] = 0
			} else {
				rates[t] = float64(hits) / total
			}
		}
	}
	caveats := "n=" + fmt.Sprint(len(ScenariosV1)) + "; hash=" + ScenariosHash()
	if pure {
		caveats += "; --pure: oracles disabled"
	} else {
		caveats += "; oracles may be active — use --pure for clean measurement"
	}
	judge := os.Getenv("PALATE_JUDGE")
	if judge == "" {
		judge = "unconfigured (set PALATE_JUDGE)"
	}
	return Report{
		Version:       ScenariosVersion,
		ScenariosHash: ScenariosHash(),
		Judge:         judge,
		Pure:          pure,
		CaptureRates:  rates,
		Caveats:       caveats,
	}, nil
}

func RunRetrieval(conn *sql.DB, pure bool) (Report, error) {
	return RunCapture(conn, pure)
}

func IndependenceCheck() []string {
	var issues []string
	if os.Getenv("PALATE_JUDGE") == "" {
		issues = append(issues, "PALATE_JUDGE not set — judge identity unrecorded")
	}
	got := ScenariosHash()
	if got == "" {
		issues = append(issues, "scenarios hash empty")
	}
	return issues
}
