package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	mu sync.RWMutex
	db *sql.DB
)

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
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_pragma=foreign_keys(1)", path)
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
		`CREATE TABLE IF NOT EXISTS triples (
			id TEXT PRIMARY KEY,
			subject TEXT NOT NULL,
			predicate TEXT NOT NULL,
			object TEXT NOT NULL,
			valid_from TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			valid_until TEXT DEFAULT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS facts (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			category TEXT DEFAULT '',
			confidence REAL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relation TEXT NOT NULL DEFAULT 'related',
			weight REAL DEFAULT 0.5,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
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
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_working USING fts5(content, content='working_memory', content_rowid='rowid', tokenize='porter unicode61')`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_episodic USING fts5(content, content='episodic_memory', content_rowid='rowid', tokenize='porter unicode61')`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_preferences USING fts5(statement, category, content='preferences', content_rowid='rowid', tokenize='porter unicode61')`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			return fmt.Errorf("ddl failed: %w\nsql: %s", err, s)
		}
	}
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS trg_wm_insert AFTER INSERT ON working_memory BEGIN INSERT INTO fts_working(rowid, content) VALUES (new.rowid, new.content); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_wm_update AFTER UPDATE OF content ON working_memory BEGIN UPDATE fts_working SET content=new.content WHERE rowid=new.rowid; END`,
		`CREATE TRIGGER IF NOT EXISTS trg_wm_delete AFTER DELETE ON working_memory BEGIN DELETE FROM fts_working WHERE rowid=old.rowid; END`,
		`CREATE TRIGGER IF NOT EXISTS trg_em_insert AFTER INSERT ON episodic_memory BEGIN INSERT INTO fts_episodic(rowid, content) VALUES (new.rowid, new.content); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_em_delete AFTER DELETE ON episodic_memory BEGIN DELETE FROM fts_episodic WHERE rowid=old.rowid; END`,
		`CREATE TRIGGER IF NOT EXISTS trg_pref_insert AFTER INSERT ON preferences BEGIN INSERT INTO fts_preferences(rowid, statement, category) VALUES (new.rowid, new.statement, new.category); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_pref_update AFTER UPDATE OF statement ON preferences BEGIN UPDATE fts_preferences SET statement=new.statement, category=new.category WHERE rowid=new.rowid; END`,
		`CREATE TRIGGER IF NOT EXISTS trg_pref_delete AFTER DELETE ON preferences BEGIN DELETE FROM fts_preferences WHERE rowid=old.rowid; END`,
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
		`CREATE INDEX IF NOT EXISTS idx_triples_spo ON triples(subject, predicate, object)`,
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
	return err
}
