package store

import (
	"database/sql"
	"time"
)

// Event levels.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// AddEvent appends an activity-feed event. Missing optional ids are stored NULL.
func (s *Store) AddEvent(ev Event) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.Level == "" {
		ev.Level = LevelInfo
	}
	_, err := s.db.Exec(`
		INSERT INTO events (ts, level, kind, subscription_id, model_id, version_id, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		formatTime(ev.TS), ev.Level, ev.Kind,
		nullInt64(ev.SubscriptionID), nullInt(ev.ModelID), nullInt(ev.VersionID), ev.Message)
	return err
}

// RecentEvents returns the newest events, capped at limit.
func (s *Store) RecentEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, ts, level, kind, subscription_id, model_id, version_id, message
		FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			ev    Event
			ts    string
			subID sql.NullInt64
			modID sql.NullInt64
			verID sql.NullInt64
		)
		if err := rows.Scan(&ev.ID, &ts, &ev.Level, &ev.Kind, &subID, &modID, &verID, &ev.Message); err != nil {
			return nil, err
		}
		ev.TS = parseTime(ts)
		if subID.Valid {
			ev.SubscriptionID = &subID.Int64
		}
		if modID.Valid {
			v := int(modID.Int64)
			ev.ModelID = &v
		}
		if verID.Valid {
			v := int(verID.Int64)
			ev.VersionID = &v
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
