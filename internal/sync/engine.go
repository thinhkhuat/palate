package sync

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type Event struct {
	ID         string  `json:"id"`
	Op         string  `json:"op"`
	MemoryID   string  `json:"memory_id"`
	Timestamp  string  `json:"timestamp"`
	DeviceID   string  `json:"device_id"`
	Payload    string  `json:"payload"`
	Parents    []string `json:"parents"`
	Importance float64 `json:"importance"`
	Hash       string  `json:"hash"`
}

func EventHash(memoryID, op, ts, deviceID, payload string, parents []string, importance float64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%v|%.10g", memoryID, op, ts, deviceID, payload, parents, importance)
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func LogEvent(db *sql.DB, op, memoryID, deviceID, payload string, parents []string, importance float64) (string, error) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	hash := EventHash(memoryID, op, ts, deviceID, payload, parents, importance)
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	id := fmt.Sprintf("%x-%s", idBytes, hash[:8])
	parentsJSON, _ := json.Marshal(parents)
	_, err := db.Exec(`INSERT INTO events(event_type, payload, created_at) VALUES(?,?,?)`,
		op, fmt.Sprintf(`{"id":%q,"memory_id":%q,"device_id":%q,"hash":%q,"parents":%s,"importance":%.10g,"payload":%q}`, id, memoryID, deviceID, hash, string(parentsJSON), importance, payload), ts)
	if err != nil {
		return "", err
	}
	return id, nil
}

func PullChanges(db *sql.DB, cursor string, limit int) ([]Event, string, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if cursor == "" {
		rows, err = db.Query(`SELECT id, event_type, payload, created_at FROM events ORDER BY created_at ASC, id ASC LIMIT ?`, limit+1)
	} else {
		rows, err = db.Query(`SELECT id, event_type, payload, created_at FROM events WHERE (created_at, id) > (?, ?) ORDER BY created_at ASC, id ASC LIMIT ?`, cursor, cursor, limit+1)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.Op, &payload, &e.Timestamp); err != nil {
			continue
		}
		e.Payload = payload
		events = append(events, e)
	}
	nextCursor := ""
	if len(events) > limit {
		events = events[:limit]
		nextCursor = events[len(events)-1].Timestamp
	}
	return events, nextCursor, nil
}

func PushChanges(db *sql.DB, events []Event) error {
	for _, e := range events {
		var exists string
		err := db.QueryRow(`SELECT id FROM events WHERE id=?`, e.ID).Scan(&exists)
		if err == nil {
			continue
		}
		parentsJSON, _ := json.Marshal(e.Parents)
		payload := e.Payload
		if payload == "" {
			payload = "{}"
		}
		ts := e.Timestamp
		if ts == "" {
			ts = time.Now().UTC().Format(time.RFC3339Nano)
		}
		_, err = db.Exec(`INSERT INTO events(id, event_type, payload, created_at) VALUES(?,?,?,?)`,
			e.ID, e.Op, fmt.Sprintf(`{"payload":%q,"parents":%s}`, payload, string(parentsJSON)), ts)
		if err != nil {
			return err
		}
	}
	return nil
}

func SortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
