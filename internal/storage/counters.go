package storage

import "database/sql"

func BumpHit(conn *sql.DB, signalType string) {
	_, _ = conn.Exec(`
		INSERT INTO capture_counters(signal_type, hits, drops) VALUES(?,1,0)
		ON CONFLICT(signal_type) DO UPDATE SET hits=hits+1, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`, signalType)
}

func BumpDrop(conn *sql.DB, signalType string) {
	_, _ = conn.Exec(`
		INSERT INTO capture_counters(signal_type, hits, drops) VALUES(?,0,1)
		ON CONFLICT(signal_type) DO UPDATE SET drops=drops+1, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')`, signalType)
}
