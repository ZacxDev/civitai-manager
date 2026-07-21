package store

import "time"

// Kind is the subscription target type.
type Kind string

const (
	// KindModel subscribes to a single model id.
	KindModel Kind = "model"
	// KindCreator subscribes to all models by a creator username.
	KindCreator Kind = "creator"
)

// QueueStatus is a download_queue row's lifecycle state.
type QueueStatus string

const (
	StatusQueued      QueueStatus = "queued"
	StatusDownloading QueueStatus = "downloading"
	StatusDone        QueueStatus = "done"
	StatusFailed      QueueStatus = "failed"
	StatusSkipped     QueueStatus = "skipped"
)

// Subscription is a row of the subscriptions table.
type Subscription struct {
	ID               int64
	Kind             Kind
	ModelID          *int
	Username         string
	AutoDownload     bool
	NotifyOnly       bool
	Layout           string
	BaseModelFilter  string
	FileTypePref     string
	PollIntervalSecs int
	LastPolledAt     *time.Time
	CreatedAt        time.Time
}

// Label returns a human-readable identifier for the subscription target.
func (s Subscription) Label() string {
	if s.Kind == KindCreator {
		return "@" + s.Username
	}
	if s.ModelID != nil {
		return "model " + itoa(*s.ModelID)
	}
	return "subscription"
}

// PollInterval returns the poll cadence as a duration.
func (s Subscription) PollInterval() time.Duration {
	return time.Duration(s.PollIntervalSecs) * time.Second
}

// QueueItem is a row of the download_queue table.
type QueueItem struct {
	ID             int64
	SubscriptionID *int64
	ModelID        int
	VersionID      int
	FileID         int
	FileName       string
	DownloadURL    string
	DestPath       string
	Status         QueueStatus
	BytesDone      int64
	SizeKB         float64
	SHA256Expected string
	SHA256Actual   string
	Attempts       int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Event is a row of the events table (the UI activity feed).
type Event struct {
	ID             int64
	TS             time.Time
	Level          string
	Kind           string
	SubscriptionID *int64
	ModelID        *int
	VersionID      *int
	Message        string
}

// LocalFile is a row of the local_files library index.
type LocalFile struct {
	Path         string
	SHA256       string
	AutoV2       string
	ModelID      *int
	VersionID    *int
	SizeBytes    int64
	IsSuperseded bool
	MatchedAt    *time.Time
}

func itoa(i int) string {
	// small local helper to avoid importing strconv in the hot Label path
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
