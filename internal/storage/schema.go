package storage

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	mu sync.RWMutex
	db *sql.DB

	// dirty is set by AppendEvent — the single funnel every mutation passes
	// through — and cleared by FlushIfDirty. onDirtyFlush is registered once
	// per process by the CLI and the MCP server (it renders TASTE.md); the
	// storage package never imports render, so the callback breaks the cycle.
	dirtyMu      sync.Mutex
	dirty        bool
	onDirtyFlush func(*sql.DB)
)

// SetOnDirtyFlush registers the callback run by FlushIfDirty and Close when
// a mutation happened since the last flush. nil disables flushing.
func SetOnDirtyFlush(fn func(*sql.DB)) {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	onDirtyFlush = fn
}

// FlushIfDirty runs the registered callback once if any mutation was logged
// since the last flush. Long-lived processes (the MCP server) call it after
// each write tool; everything else gets it for free from Close.
func FlushIfDirty() {
	dirtyMu.Lock()
	run := dirty && onDirtyFlush != nil && db != nil
	fn, conn := onDirtyFlush, db
	if run {
		dirty = false
	}
	dirtyMu.Unlock()
	if run {
		fn(conn)
	}
}

func markDirty() {
	dirtyMu.Lock()
	dirty = true
	dirtyMu.Unlock()
}

func Open(path string) (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if db != nil {
		return db, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// modernc.org/sqlite only honours _pragma=name(value); the mattn-style
	// _journal_mode/_busy_timeout keys are silently ignored, which left the DB in
	// rollback-journal mode with a zero busy timeout.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := initSchema(conn); err != nil {
		conn.Close()
		return nil, err
	}
	_ = os.Chmod(dir, 0700)
	_ = os.Chmod(path, 0600)
	db = conn
	return db, nil
}

func Close() {
	FlushIfDirty()
	mu.Lock()
	defer mu.Unlock()
	if db != nil {
		db.Close()
		db = nil
	}
}

func initSchema(conn *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS working_memory (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			metadata TEXT DEFAULT '{}',
			importance REAL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			session_id TEXT DEFAULT '',
			tags TEXT DEFAULT '[]',
			consolidated_at TEXT DEFAULT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS episodic_memory (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			metadata TEXT DEFAULT '{}',
			importance REAL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			session_id TEXT DEFAULT '',
			tags TEXT DEFAULT '[]',
			source TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS scratchpad (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS consolidation_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_table TEXT NOT NULL,
			from_id TEXT NOT NULL,
			to_table TEXT NOT NULL,
			to_id TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			as_of TEXT DEFAULT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS preferences (
			id TEXT PRIMARY KEY,
			category TEXT NOT NULL,
			statement TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0.5,
			importance REAL NOT NULL DEFAULT 0.5,
			source TEXT NOT NULL DEFAULT 'stated_preference',
			provenance TEXT NOT NULL DEFAULT '[]',
			evolution TEXT DEFAULT '',
			valid_from TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			valid_until TEXT DEFAULT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			last_reinforced_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			reinforcement_count INTEGER NOT NULL DEFAULT 0,
			last_verified_at TEXT DEFAULT NULL,
			owner TEXT DEFAULT '',
			trust_tier TEXT NOT NULL DEFAULT 'STATED',
			veracity TEXT NOT NULL DEFAULT 'stated'
		)`,
		`CREATE TABLE IF NOT EXISTS conflicts (
			id TEXT PRIMARY KEY,
			preference_id TEXT NOT NULL REFERENCES preferences(id),
			conflicting_id TEXT NOT NULL REFERENCES preferences(id),
			reason TEXT DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			resolved_at TEXT DEFAULT NULL
		)`,
		// Dead scaffold from the mnemosyne-shaped early schema: no code
		// path ever read or wrote facts/triples/graph_edges (verified
		// 2026-09-16 — zero Go callers outside this DDL, zero rows in the
		// live store). Dropped idempotently on open; decisions.md records
		// the verdict. Load-bearing mnemosyne-shaped tables stay:
		// working_memory / episodic_memory / scratchpad (Remember, Forget,
		// Sleep, Recall), memory_embeddings (vec seam), plus the events,
		// preferences, conflicts, proposals and FTS machinery.
		`DROP TABLE IF EXISTS facts`,
		`DROP INDEX IF EXISTS idx_triples_spo`,
		`DROP TABLE IF EXISTS triples`,
		`DROP TABLE IF EXISTS graph_edges`,
		`CREATE TABLE IF NOT EXISTS proposals (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			category TEXT DEFAULT '',
			source TEXT NOT NULL DEFAULT 'llm',
			proposed_weight REAL DEFAULT 0.4,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS memory_embeddings (
			memory_id TEXT PRIMARY KEY,
			table_name TEXT NOT NULL,
			vector_json TEXT NOT NULL DEFAULT '[]',
			dim INTEGER DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS capture_counters (
			signal_type TEXT PRIMARY KEY,
			hits INTEGER NOT NULL DEFAULT 0,
			drops INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS consolidations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			last_event_id INTEGER NOT NULL DEFAULT 0,
			actions INTEGER NOT NULL DEFAULT 0,
			mode TEXT NOT NULL DEFAULT 'auto',
			at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS learn_cursors (
			source TEXT NOT NULL,
			key TEXT NOT NULL,
			position TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			PRIMARY KEY (source, key)
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_working USING fts5(content, content='working_memory', content_rowid='rowid', tokenize='porter unicode61')`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_episodic USING fts5(content, content='episodic_memory', content_rowid='rowid', tokenize='porter unicode61')`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_preferences USING fts5(statement, category, content='preferences', content_rowid='rowid', tokenize='porter unicode61')`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			return fmt.Errorf("ddl failed: %w\nsql: %s", err, s)
		}
	}
	// External-content FTS5 tables (content='…') must be kept in sync with the
	// documented 'delete' + insert pair; a plain UPDATE/DELETE on the FTS table
	// raises SQLITE_CORRUPT_VTAB (267). The update/delete triggers are dropped
	// and recreated so stores created by earlier versions pick up the fix.
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS trg_wm_insert AFTER INSERT ON working_memory BEGIN INSERT INTO fts_working(rowid, content) VALUES (new.rowid, new.content); END`,
		`DROP TRIGGER IF EXISTS trg_wm_update`,
		`CREATE TRIGGER trg_wm_update AFTER UPDATE OF content ON working_memory BEGIN INSERT INTO fts_working(fts_working, rowid, content) VALUES ('delete', old.rowid, old.content); INSERT INTO fts_working(rowid, content) VALUES (new.rowid, new.content); END`,
		`DROP TRIGGER IF EXISTS trg_wm_delete`,
		`CREATE TRIGGER trg_wm_delete AFTER DELETE ON working_memory BEGIN INSERT INTO fts_working(fts_working, rowid, content) VALUES ('delete', old.rowid, old.content); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_em_insert AFTER INSERT ON episodic_memory BEGIN INSERT INTO fts_episodic(rowid, content) VALUES (new.rowid, new.content); END`,
		`DROP TRIGGER IF EXISTS trg_em_update`,
		`CREATE TRIGGER trg_em_update AFTER UPDATE OF content ON episodic_memory BEGIN INSERT INTO fts_episodic(fts_episodic, rowid, content) VALUES ('delete', old.rowid, old.content); INSERT INTO fts_episodic(rowid, content) VALUES (new.rowid, new.content); END`,
		`DROP TRIGGER IF EXISTS trg_em_delete`,
		`CREATE TRIGGER trg_em_delete AFTER DELETE ON episodic_memory BEGIN INSERT INTO fts_episodic(fts_episodic, rowid, content) VALUES ('delete', old.rowid, old.content); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_pref_insert AFTER INSERT ON preferences BEGIN INSERT INTO fts_preferences(rowid, statement, category) VALUES (new.rowid, new.statement, new.category); END`,
		`DROP TRIGGER IF EXISTS trg_pref_update`,
		`CREATE TRIGGER trg_pref_update AFTER UPDATE OF statement, category ON preferences BEGIN INSERT INTO fts_preferences(fts_preferences, rowid, statement, category) VALUES ('delete', old.rowid, old.statement, old.category); INSERT INTO fts_preferences(rowid, statement, category) VALUES (new.rowid, new.statement, new.category); END`,
		`DROP TRIGGER IF EXISTS trg_pref_delete`,
		`CREATE TRIGGER trg_pref_delete AFTER DELETE ON preferences BEGIN INSERT INTO fts_preferences(fts_preferences, rowid, statement, category) VALUES ('delete', old.rowid, old.statement, old.category); END`,
	}
	for _, t := range triggers {
		if _, err := conn.Exec(t); err != nil {
			return fmt.Errorf("trigger failed: %w", err)
		}
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_wm_session ON working_memory(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_wm_consolidated ON working_memory(consolidated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pref_category ON preferences(category)`,
		`CREATE INDEX IF NOT EXISTS idx_pref_confidence ON preferences(confidence)`,
		`CREATE INDEX IF NOT EXISTS idx_pref_valid ON preferences(valid_until)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type)`,
	}
	for _, idx := range indexes {
		if _, err := conn.Exec(idx); err != nil {
			return fmt.Errorf("index failed: %w", err)
		}
	}
	if err := ensureColumns(conn); err != nil {
		return err
	}
	return nil
}

func ensureColumns(conn *sql.DB) error {
	cols := map[string][]string{
		"preferences": {"trust_tier TEXT NOT NULL DEFAULT 'STATED'", "veracity TEXT NOT NULL DEFAULT 'stated'"},
	}
	for table, adds := range cols {
		rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			continue
		}
		existing := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			_ = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			existing[name] = true
		}
		rows.Close()
		for _, col := range adds {
			colName := col
			if idx := indexOf(col, " "); idx > 0 {
				colName = col[:idx]
			}
			if !existing[colName] {
				q := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, col)
				if _, err := conn.Exec(q); err != nil {
					return fmt.Errorf("add column %s.%s: %w", table, colName, err)
				}
			}
		}
	}
	return nil
}

func indexOf(s, sep string) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}

func DB() *sql.DB { return db }

func AppendEvent(conn *sql.DB, eventType, payload string) error {
	_, err := conn.Exec(`INSERT INTO events(event_type, payload) VALUES(?,?)`, eventType, payload)
	if err == nil {
		markDirty()
	}
	return err
}

// BackupBefore copies the DB file to <backupDir>/palate.db-<YYYYMMDD-HHMM>-<label>
// ahead of a destructive verb (forget, pull). A missing DB is not an error —
// there is nothing to protect yet. Returns the backup path ("" when skipped).
func BackupBefore(dbPath, backupDir, label string) (string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", err
	}
	dst := filepath.Join(backupDir, fmt.Sprintf("%s-%s-%s", filepath.Base(dbPath), time.Now().Format("20060102-1504"), label))
	// Minute-stamped: a second call in the same minute (a loop of forget /
	// promote) must keep the FIRST snapshot — that is the state to restore.
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := vacuumBackup(dbPath, dst); err != nil {
		// VACUUM INTO may have left a partial file; it must never be taken
		// for a snapshot.
		_ = os.Remove(dst)
		// A real SQLite file that cannot be vacuumed fails the backup: a raw
		// copy of the main file misses whatever still sits in the -wal, and
		// the caller's destructive write must not run on a backup that is
		// silently incomplete. Only a file that is not a SQLite database at
		// all (a test fixture) has no WAL to lose, so a raw copy is faithful.
		if isSQLiteFile(dbPath) {
			return "", fmt.Errorf("backup of %s: VACUUM INTO failed, not falling back to a raw copy that would miss the WAL: %w", dbPath, err)
		}
		if cerr := rawCopyBackup(dbPath, dst); cerr != nil {
			return "", cerr
		}
	}
	return dst, os.Chmod(dst, 0600)
}

// vacuumBackup runs `VACUUM INTO` through a fresh, short-lived connection to
// dbPath (red-team M1). Unlike a raw byte copy of the main DB file, VACUUM
// INTO reads through the WAL like any other connection, so a commit an open
// writer (the long-lived MCP `serve` process) has not yet checkpointed is
// still in the snapshot. dst must not already exist — VACUUM INTO refuses to
// overwrite a file.
func vacuumBackup(dbPath, dst string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer conn.Close()
	q := "VACUUM INTO '" + strings.ReplaceAll(dst, "'", "''") + "'"
	_, err = conn.Exec(q)
	return err
}

// isSQLiteFile reports whether path starts with the SQLite file header.
func isSQLiteFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return string(head) == "SQLite format 3\x00"
}

func rawCopyBackup(dbPath, dst string) error {
	src, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// NewestBackup returns the most recently modified file in backupDir, or "".
func NewestBackup(backupDir string) string {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return ""
	}
	var newest string
	var newestAt time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = filepath.Join(backupDir, e.Name()), info.ModTime()
		}
	}
	return newest
}
