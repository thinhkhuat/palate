package distill

import "database/sql"

// GetCursor returns the saved position for one evidence stream, identified
// by source (harness/source name, e.g. "claude_code", "git", "memsearch")
// and key (the transcript/repo/journal path uniquely identifying it), plus
// the count of consecutive failed distill attempts against the current
// window. ok is false when no row exists yet.
func GetCursor(conn *sql.DB, source, key string) (position string, attempts int, ok bool) {
	err := conn.QueryRow(`SELECT position, attempts FROM learn_cursors WHERE source=? AND key=?`, source, key).Scan(&position, &attempts)
	if err != nil {
		return "", 0, false
	}
	return position, attempts, true
}

// SetCursor upserts the position for one evidence stream and resets its
// attempts counter to 0 — a successful advance clears any prior failure
// streak.
func SetCursor(conn *sql.DB, source, key, position string) error {
	_, err := conn.Exec(`
		INSERT INTO learn_cursors(source, key, position, attempts) VALUES(?,?,?,0)
		ON CONFLICT(source, key) DO UPDATE SET position=excluded.position, attempts=0, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
		source, key, position)
	return err
}

// RecordFailedAttempt increments the attempts counter for one evidence
// stream's current window without moving its position, and returns the new
// count. Callers (e.g. the session learner) abandon a
// poisoned window after a small number of consecutive failures rather than
// retrying it forever.
func RecordFailedAttempt(conn *sql.DB, source, key string) (attempts int, err error) {
	_, err = conn.Exec(`
		INSERT INTO learn_cursors(source, key, position, attempts) VALUES(?,?,'',1)
		ON CONFLICT(source, key) DO UPDATE SET attempts=attempts+1, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
		source, key)
	if err != nil {
		return 0, err
	}
	err = conn.QueryRow(`SELECT attempts FROM learn_cursors WHERE source=? AND key=?`, source, key).Scan(&attempts)
	return attempts, err
}
