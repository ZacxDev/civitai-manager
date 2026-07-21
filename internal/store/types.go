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
	// NotBefore, when set, gates when the worker may claim this row: it stays
	// unclaimable until the wall clock reaches NotBefore. Nil (the default, and
	// all manual/backfill downloads) means immediately claimable. The poller
	// sets a small random offset on auto-detected downloads so a fleet of
	// installs de-synchronizes its download starts (anti-stampede).
	NotBefore *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
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
	// ID is the row's SQLite rowid (populated on read; ignored on upsert, which
	// is keyed by Path).
	ID           int64
	Path         string
	SHA256       string
	AutoV2       string
	ModelID      *int
	VersionID    *int
	SizeBytes    int64
	IsSuperseded bool
	// Mtime is the file's modification time captured at scan (for the
	// incremental hash cache). Nil when unknown.
	Mtime *time.Time
	// Status is the match state: LocalStatusMatched, LocalStatusUnmatched,
	// LocalStatusUnmatchedPending, or LocalStatusBroken.
	Status string
	// CandidateReason is the deletion-candidate flag (CandidateSuperseded,
	// CandidateDuplicate, CandidateBroken) or "" when the file is not a candidate.
	CandidateReason string
	// Kind is LocalKindModel for a model-weight file or LocalKindSidecar for a
	// tracked broken non-model file (stray part/empty info/orphan preview).
	Kind      string
	MatchedAt *time.Time
}

// Local-file match statuses.
const (
	LocalStatusMatched          = "matched"
	LocalStatusUnmatched        = "unmatched"
	LocalStatusUnmatchedPending = "unmatched-pending"
	LocalStatusBroken           = "broken"
)

// Deletion-candidate reasons.
const (
	CandidateSuperseded = "superseded"
	CandidateDuplicate  = "duplicate"
	CandidateBroken     = "broken"
)

// Local-file kinds.
const (
	LocalKindModel   = "model"
	LocalKindSidecar = "sidecar"
)

// IsCandidate reports whether the file is flagged for quarantine.
func (lf LocalFile) IsCandidate() bool { return lf.CandidateReason != "" }

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
