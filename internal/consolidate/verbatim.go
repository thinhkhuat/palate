package consolidate

import (
	"fmt"
	"strings"
	"time"

	"database/sql"

	"github.com/thinhkhuat/palate/internal/storage"
)

// IsVerbatimRow reports whether a stored statement is a verbatim work-product
// dump of the kind the retired file-diff path wrote: a "prefers:"-prefixed
// row (the old summarizeDiff shape), a code/content-shaped blob (regex, pipe
// tables, def/func lines, JSON fragments), or a over-long pasted paragraph.
// The detached LLM distiller writes prose; verbatim rows are its opposite.
// RetireVerbatimRows closes every live match through the events funnel and
// returns the retired ids. dryRun counts without writing.
func IsVerbatimRow(statement string) bool {
	s := strings.TrimSpace(statement)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "prefers:") {
		return true
	}
	markers := []string{
		"re.compile(", "func ", "def ", "=>", ":=", "SELECT ",
		"INSERT INTO", "sha256", "\"sha256\"",
		"VENDORED FROM", "| Rail", "TABLE_SEP_RE", "NUMBERED_RE",
		"DATE_ISO_RE", "PERCENT_RE", "sys.stderr.write",
		"\"version\":", "\"tarball\":", "\"generated_at\":",
		"const ", "var ", "import ", "package ",
	}
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	// A pasted paragraph/heading dump: very long single statement that reads
	// as document body rather than a preference sentence.
	if len([]rune(s)) > 400 && (strings.Contains(s, " / ") || strings.Count(s, ".") > 6) {
		return true
	}
	return false
}

// RetireVerbatimRows closes live verbatim rows through the events funnel.
// It returns the retired ids; dryRun only counts.
func RetireVerbatimRows(conn *sql.DB, dryRun bool) ([]string, error) {
	rows, err := conn.Query(`SELECT id, statement FROM preferences WHERE valid_until IS NULL`)
	if err != nil {
		return nil, err
	}
	type match struct {
		id, stmt string
	}
	var matches []match
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.id, &m.stmt); err != nil {
			continue
		}
		if IsVerbatimRow(m.stmt) {
			matches = append(matches, m)
		}
	}
	rows.Close()
	if dryRun {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.id)
		}
		return ids, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var retired []string
	for _, m := range matches {
		if _, err := conn.Exec(`UPDATE preferences SET valid_until=? WHERE id=? AND valid_until IS NULL`, now, m.id); err != nil {
			continue
		}
		_ = storage.AppendEvent(conn, "consolidate_retire", fmt.Sprintf(`{"id":%q,"reason":%q}`, m.id, "verbatim work-product dump, not a preference"))
		retired = append(retired, m.id)
	}
	return retired, nil
}
