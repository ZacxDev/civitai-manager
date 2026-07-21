package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a lookup by id yields no row.
var ErrNotFound = errors.New("not found")

// CreateSubscription inserts a subscription and returns its id. A duplicate
// target (same model_id or username) fails on the unique index.
func (s *Store) CreateSubscription(sub Subscription) (int64, error) {
	if sub.Layout == "" {
		sub.Layout = "default"
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.Exec(`
		INSERT INTO subscriptions
			(kind, model_id, username, auto_download, notify_only, layout,
			 base_model_filter, file_type_pref, poll_interval_secs, last_polled_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(sub.Kind), nullInt(sub.ModelID), nullStr(sub.Username),
		boolToInt(sub.AutoDownload), boolToInt(sub.NotifyOnly), sub.Layout,
		nullStr(sub.BaseModelFilter), nullStr(sub.FileTypePref),
		sub.PollIntervalSecs, nullTimeStr(sub.LastPolledAt), formatTime(sub.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert subscription: %w", err)
	}
	return res.LastInsertId()
}

const subCols = `id, kind, model_id, username, auto_download, notify_only, layout,
	base_model_filter, file_type_pref, poll_interval_secs, last_polled_at, created_at`

func scanSubscription(sc scanner) (Subscription, error) {
	var (
		sub          Subscription
		modelID      sql.NullInt64
		username     sql.NullString
		baseFilter   sql.NullString
		fileTypePref sql.NullString
		lastPolled   sql.NullString
		createdAt    string
		kind         string
	)
	if err := sc.Scan(&sub.ID, &kind, &modelID, &username, &sub.AutoDownload,
		&sub.NotifyOnly, &sub.Layout, &baseFilter, &fileTypePref,
		&sub.PollIntervalSecs, &lastPolled, &createdAt); err != nil {
		return Subscription{}, err
	}
	sub.Kind = Kind(kind)
	if modelID.Valid {
		v := int(modelID.Int64)
		sub.ModelID = &v
	}
	sub.Username = username.String
	sub.BaseModelFilter = baseFilter.String
	sub.FileTypePref = fileTypePref.String
	if lastPolled.Valid {
		t := parseTime(lastPolled.String)
		sub.LastPolledAt = &t
	}
	sub.CreatedAt = parseTime(createdAt)
	return sub, nil
}

// GetSubscription fetches one subscription by id.
func (s *Store) GetSubscription(id int64) (*Subscription, error) {
	row := s.db.QueryRow(`SELECT `+subCols+` FROM subscriptions WHERE id = ?`, id)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// ListSubscriptions returns all subscriptions, newest first.
func (s *Store) ListSubscriptions() ([]Subscription, error) {
	rows, err := s.db.Query(`SELECT ` + subCols + ` FROM subscriptions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// FindModelSubscription returns the subscription for a model id, or ErrNotFound.
func (s *Store) FindModelSubscription(modelID int) (*Subscription, error) {
	row := s.db.QueryRow(`SELECT `+subCols+` FROM subscriptions WHERE model_id = ?`, modelID)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// FindCreatorSubscription returns the subscription for a username, or ErrNotFound.
func (s *Store) FindCreatorSubscription(username string) (*Subscription, error) {
	row := s.db.QueryRow(`SELECT `+subCols+` FROM subscriptions WHERE username = ?`, username)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// DeleteSubscription removes a subscription and ALL of its per-subscription
// state — its seen_versions ledger AND its download_queue rows — so a fresh
// re-subscribe to the same target is a clean slate.
//
// This must delete the queue rows EXPLICITLY (not rely on the FK): the
// download_queue → subscriptions FK is ON DELETE SET NULL, not CASCADE, so a
// terminal 'done' row would otherwise survive the delete with subscription_id
// NULLed. That stale row keeps a slot in the ux_dlq_active partial-unique index
// on (version_id, file_id), so after the user unsubscribes and deletes the file
// from disk, a re-subscribe's re-enqueue hits ON CONFLICT DO NOTHING and the
// version is never re-downloaded ("No file downloaded"). seen_versions is
// ON DELETE CASCADE, but we clear it explicitly too so the cleanup is
// self-contained and order-independent. All three deletes run in one
// transaction, scoped strictly to this subscription id.
func (s *Store) DeleteSubscription(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM download_queue WHERE subscription_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM seen_versions WHERE subscription_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM subscriptions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetSubscriptionFlags updates the auto_download / notify_only toggles.
func (s *Store) SetSubscriptionFlags(id int64, autoDownload, notifyOnly bool) error {
	res, err := s.db.Exec(`UPDATE subscriptions SET auto_download = ?, notify_only = ? WHERE id = ?`,
		boolToInt(autoDownload), boolToInt(notifyOnly), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchPolled records the time a subscription was last polled.
func (s *Store) TouchPolled(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET last_polled_at = ? WHERE id = ?`,
		formatTime(at), id)
	return err
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}
