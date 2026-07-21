package store

import "time"

// SeenVersionIDs returns the set of version ids already recorded for a
// subscription (the diff ledger). A brand-new subscription returns an empty set.
func (s *Store) SeenVersionIDs(subID int64) (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT version_id FROM seen_versions WHERE subscription_id = ?`, subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		seen[v] = true
	}
	return seen, rows.Err()
}

// CountSeen returns how many versions have been recorded for a subscription.
// Zero means the subscription has never been polled (first-poll seeding path).
func (s *Store) CountSeen(subID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM seen_versions WHERE subscription_id = ?`, subID).Scan(&n)
	return n, err
}

// MarkSeen records a version id as observed for a subscription. It is
// idempotent: re-marking an already-seen (subscription, version) is a no-op.
// publishedAt may be the zero time (the summary endpoint does not carry it).
func (s *Store) MarkSeen(subID int64, versionID int, publishedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO seen_versions (subscription_id, version_id, published_at, first_seen_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (subscription_id, version_id) DO NOTHING`,
		subID, versionID, nullTimeStr(nonZero(publishedAt)), formatTime(time.Now().UTC()))
	return err
}

func nonZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
