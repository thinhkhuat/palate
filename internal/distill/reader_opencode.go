package distill

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// openCodeReader queries OpenCode's own sqlite db directly, via a
// dedicated read-only connection — never through internal/storage.Open,
// which is a process-wide singleton for Palate's own db.
type openCodeReader struct{}

func (openCodeReader) Harness() string { return "opencode" }

func (r openCodeReader) defaultDBPath() string {
	return filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
}

func openCodeReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
}

func (r openCodeReader) Discover(root string) ([]Transcript, error) {
	dbPath := root
	if dbPath == "" {
		dbPath = r.defaultDBPath()
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}

	conn, err := openCodeReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	rows, err := conn.Query(`
		SELECT s.id, s.directory, MAX(m.time_created)
		FROM session s
		JOIN message m ON m.session_id = s.id
		GROUP BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Transcript
	for rows.Next() {
		var sessionID, directory string
		var lastMs int64
		if err := rows.Scan(&sessionID, &directory, &lastMs); err != nil {
			continue
		}
		out = append(out, Transcript{
			Harness: r.Harness(),
			Project: directory,
			Path:    "opencode:" + dbPath + ":" + sessionID,
			ModTime: time.UnixMilli(lastMs),
		})
	}
	return out, rows.Err()
}

func (r openCodeReader) Messages(t Transcript) ([]Message, error) {
	dbPath, sessionID, ok := parseOpenCodePath(t.Path)
	if !ok {
		return nil, fmt.Errorf("opencode: not an opencode transcript path: %q", t.Path)
	}

	conn, err := openCodeReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// synthetic exclusion is required: OpenCode injects role:"user" rows
	// (e.g. a continuation prompt) marked synthetic:true that are not the
	// human's own words.
	rows, err := conn.Query(`
		SELECT json_extract(m.data,'$.role'), json_extract(p.data,'$.text')
		FROM message m
		JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		  AND json_extract(p.data,'$.type') = 'text'
		  AND json_extract(p.data,'$.synthetic') IS NOT 1
		ORDER BY m.time_created, p.time_created`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	idx := 0
	for rows.Next() {
		var role, text string
		if err := rows.Scan(&role, &text); err != nil {
			continue
		}
		if text == "" || (role != "user" && role != "assistant") {
			continue
		}
		out = append(out, Message{Role: role, Content: text, Index: idx})
		idx++
	}
	return out, rows.Err()
}

func parseOpenCodePath(path string) (dbPath, sessionID string, ok bool) {
	const prefix = "opencode:"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := path[len(prefix):]
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}
