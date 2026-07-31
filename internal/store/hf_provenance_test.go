package store

import (
	"database/sql"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// openStoreAtVersion builds a REAL database with every migration UP TO AND
// INCLUDING maxVersion applied (and the schema_migrations bookkeeping to match),
// then hands back the raw handle. It is how the 0015 test proves the migration
// applies to a PRE-EXISTING, POPULATED database rather than only to a fresh one:
// a CREATE TABLE that works on an empty file can still collide with real data.
func openStoreAtVersion(t *testing.T, path string, maxVersion int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("migration version %s: %v", name, err)
		}
		if v > maxVersion {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, nowRFC3339()); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	return db
}

// TestHFProvenanceMigrationOnPopulatedDB applies 0015 to a database that is
// already at 0014 AND already carries local-file rows, then confirms the old
// data is untouched, the new table exists, and re-opening is idempotent.
func TestHFProvenanceMigrationOnPopulatedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "populated.db")

	db := openStoreAtVersion(t, path, 14)
	if _, err := db.Exec(`INSERT INTO local_files
		(path, sha256, size_bytes, is_superseded, status, candidate_reason, kind, matched_at, scan_root)
		VALUES (?, ?, ?, 0, 'unmatched', '', 'model', ?, '')`,
		"/models/loras/a.safetensors", "aa11", int64(10), nowRFC3339()); err != nil {
		t.Fatalf("seed local_files: %v", err)
	}
	var pre int
	if err := db.QueryRow(`SELECT COUNT(*) FROM local_files`).Scan(&pre); err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open (migrating 0014 -> 0015): %v", err)
	}
	defer st.Close()

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 18 {
		t.Fatalf("schema version after migrate = %d, want 18", v)
	}
	var post int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM local_files`).Scan(&post); err != nil {
		t.Fatalf("count after migrate: %v", err)
	}
	if post != pre {
		t.Fatalf("local_files rows = %d after migrate, want %d (migration must not touch existing data)", post, pre)
	}
	// The new table is usable.
	if err := st.UpsertHFProvenance(HFProvenance{
		SHA256: "aa11", Repo: "Bingsu/adetailer", Path: "face_yolov8n.pt", Revision: "53cc19de",
	}); err != nil {
		t.Fatalf("upsert after migrate: %v", err)
	}

	// Idempotent: a second Open must not re-apply 0015 (that would fail on the
	// existing table) and must not lose the row.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	got, err := st2.HFProvenanceBySHA256("aa11")
	if err != nil || len(got) != 1 {
		t.Fatalf("after reopen: rows=%d err=%v, want 1 row", len(got), err)
	}
}

// TestHFProvenanceCRUD is the table-driven round trip: write, read back by hash,
// re-write the same key (update, not duplicate), and the mirror case (same bytes
// in two repos are two TRUE statements and must both persist).
func TestHFProvenanceCRUD(t *testing.T) {
	st := newTestStore(t)
	base := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		writes   []HFProvenance
		query    string
		wantRows []HFProvenance
	}{
		{
			name:  "unknown hash yields no rows and no error",
			query: "deadbeef",
		},
		{
			name: "round trips one statement",
			writes: []HFProvenance{{
				SHA256: "70b640f8", Repo: "Bingsu/adetailer",
				Path: "face_yolov8n.pt", Revision: "53cc19de", RecordedAt: base,
			}},
			query: "70b640f8",
			wantRows: []HFProvenance{{
				SHA256: "70b640f8", Repo: "Bingsu/adetailer",
				Path: "face_yolov8n.pt", Revision: "53cc19de",
			}},
		},
		{
			name: "re-download of the same file UPDATES the revision, never duplicates",
			writes: []HFProvenance{
				{SHA256: "c0ffee01", Repo: "h94/IP-Adapter", Path: "models/x.safetensors", Revision: "old111", RecordedAt: base},
				{SHA256: "c0ffee01", Repo: "h94/IP-Adapter", Path: "models/x.safetensors", Revision: "new222", RecordedAt: base.Add(time.Hour)},
			},
			query: "c0ffee01",
			wantRows: []HFProvenance{{
				SHA256: "c0ffee01", Repo: "h94/IP-Adapter",
				Path: "models/x.safetensors", Revision: "new222",
			}},
		},
		{
			name: "mirrors: the same bytes in two repos are two true statements",
			writes: []HFProvenance{
				{SHA256: "m1rr0r", Repo: "zzz/late", Path: "w.safetensors", Revision: "r2", RecordedAt: base.Add(time.Hour)},
				{SHA256: "m1rr0r", Repo: "aaa/early", Path: "w.safetensors", Revision: "r1", RecordedAt: base},
			},
			query: "m1rr0r",
			// Ordered oldest-first: the first time we fetched these bytes.
			wantRows: []HFProvenance{
				{SHA256: "m1rr0r", Repo: "aaa/early", Path: "w.safetensors", Revision: "r1"},
				{SHA256: "m1rr0r", Repo: "zzz/late", Path: "w.safetensors", Revision: "r2"},
			},
		},
		{
			name: "hash is normalized to lowercase on write and on read",
			writes: []HFProvenance{{
				SHA256: "ABCDEF99", Repo: "stabilityai/sd-vae-ft-mse",
				Path: "diffusion_pytorch_model.safetensors", Revision: "aa", RecordedAt: base,
			}},
			query: "abcdef99",
			wantRows: []HFProvenance{{
				SHA256: "abcdef99", Repo: "stabilityai/sd-vae-ft-mse",
				Path: "diffusion_pytorch_model.safetensors", Revision: "aa",
			}},
		},
		{
			name: "a partial statement is refused (blank revision writes nothing)",
			writes: []HFProvenance{{
				SHA256: "partial1", Repo: "a/b", Path: "c.safetensors", Revision: "", RecordedAt: base,
			}},
			query: "partial1",
		},
		{
			name: "a blank repo writes nothing",
			writes: []HFProvenance{{
				SHA256: "partial2", Repo: "", Path: "c.safetensors", Revision: "rev", RecordedAt: base,
			}},
			query: "partial2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range tc.writes {
				if err := st.UpsertHFProvenance(w); err != nil {
					t.Fatalf("upsert %+v: %v", w, err)
				}
			}
			got, err := st.HFProvenanceBySHA256(tc.query)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(got) != len(tc.wantRows) {
				t.Fatalf("rows = %d (%+v), want %d", len(got), got, len(tc.wantRows))
			}
			for i, want := range tc.wantRows {
				if got[i].SHA256 != want.SHA256 || got[i].Repo != want.Repo ||
					got[i].Path != want.Path || got[i].Revision != want.Revision {
					t.Fatalf("row %d = %+v, want %+v", i, got[i], want)
				}
			}
			// HFProvenanceForFile must agree with the first row, or be nil.
			one, err := st.HFProvenanceForFile(tc.query)
			if err != nil {
				t.Fatalf("for-file: %v", err)
			}
			if len(tc.wantRows) == 0 {
				if one != nil {
					t.Fatalf("for-file = %+v, want nil", one)
				}
				return
			}
			if one == nil || one.Repo != tc.wantRows[0].Repo {
				t.Fatalf("for-file = %+v, want the first row %+v", one, tc.wantRows[0])
			}
		})
	}
}

// TestHFProvenanceSurvivesRename is the whole reason the table is keyed by
// content hash instead of by path: the file moves, and the statement about its
// BYTES is still true and still findable.
func TestHFProvenanceSurvivesRename(t *testing.T) {
	st := newTestStore(t)
	const sha = "70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c"

	if err := st.UpsertLocalFile(LocalFile{
		Path: "/models/ultralytics/bbox/face_yolov8n.pt", SHA256: sha, SizeBytes: 6230011,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertHFProvenance(HFProvenance{
		SHA256: sha, Repo: "Bingsu/adetailer", Path: "face_yolov8n.pt", Revision: "53cc19de",
	}); err != nil {
		t.Fatal(err)
	}

	// The user renames/moves the file; a re-scan indexes it at a new path (and the
	// old row goes away). Provenance is not path-keyed, so nothing about it moved.
	if _, err := st.DB().Exec(`DELETE FROM local_files WHERE path = ?`,
		"/models/ultralytics/bbox/face_yolov8n.pt"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/other/disk/renamed-detector.pt", SHA256: sha, SizeBytes: 6230011,
	}); err != nil {
		t.Fatal(err)
	}
	// And a SECOND copy of the same bytes elsewhere — one row still covers both.
	if err := st.UpsertLocalFile(LocalFile{
		Path: "/backup/face_yolov8n.pt", SHA256: sha, SizeBytes: 6230011,
	}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/other/disk/renamed-detector.pt", "/backup/face_yolov8n.pt"} {
		lf, err := st.GetLocalFileByPath(p)
		if err != nil || lf == nil {
			t.Fatalf("local file %s: %v", p, err)
		}
		got, err := st.HFProvenanceForFile(lf.SHA256)
		if err != nil {
			t.Fatalf("provenance for %s: %v", p, err)
		}
		if got == nil || got.Repo != "Bingsu/adetailer" {
			t.Fatalf("provenance for %s = %+v, want Bingsu/adetailer (hash-keyed rows survive a rename)", p, got)
		}
	}
}

// TestHFProvenanceURLs pins the two URL forms, including the /blob/ (page) shape
// verified live against huggingface.co, and the refusal to build a URL from an
// incomplete row.
func TestHFProvenanceURLs(t *testing.T) {
	tests := []struct {
		name     string
		p        HFProvenance
		wantFile string
		wantRepo string
	}{
		{
			name:     "pinned revision, root-level path",
			p:        HFProvenance{Repo: "Bingsu/adetailer", Path: "face_yolov8n.pt", Revision: "53cc19de382014514d9d4038601d261a7faa9b7b"},
			wantFile: "https://huggingface.co/Bingsu/adetailer/blob/53cc19de382014514d9d4038601d261a7faa9b7b/face_yolov8n.pt",
			wantRepo: "https://huggingface.co/Bingsu/adetailer",
		},
		{
			name:     "nested path keeps its subdirectories",
			p:        HFProvenance{Repo: "h94/IP-Adapter", Path: "models/image_encoder/model.safetensors", Revision: "018e4027"},
			wantFile: "https://huggingface.co/h94/IP-Adapter/blob/018e4027/models/image_encoder/model.safetensors",
			wantRepo: "https://huggingface.co/h94/IP-Adapter",
		},
		{
			name:     "no revision means no file URL",
			p:        HFProvenance{Repo: "a/b", Path: "x.safetensors"},
			wantFile: "",
			wantRepo: "https://huggingface.co/a/b",
		},
		{
			name:     "no repo means no URL at all",
			p:        HFProvenance{Path: "x.safetensors", Revision: "abc"},
			wantFile: "",
			wantRepo: "",
		},
		{
			// A "?" or "#" in a filename would otherwise truncate the URL into
			// something that points at a different file; a quote/space would break
			// out of the segment. Each slash-separated segment is escaped, so the
			// separators survive and nothing else does.
			name:     "hostile path characters are percent-escaped per segment",
			p:        HFProvenance{Repo: `evil org/re"po`, Path: `sub dir/x?a=1#frag.safetensors`, Revision: "r1"},
			wantFile: `https://huggingface.co/evil%20org/re%22po/blob/r1/sub%20dir/x%3Fa=1%23frag.safetensors`,
			wantRepo: `https://huggingface.co/evil%20org/re%22po`,
		},
		{
			// Traversal in a repo id survives as path segments, but the ORIGIN is a
			// constant that is never derived from a row — so the worst a hostile repo
			// id can do is address a different path on huggingface.co. The host
			// assertion below is what pins that.
			name:     "traversal segments stay inside the huggingface.co origin",
			p:        HFProvenance{Repo: "../../evil.example.com", Path: "x", Revision: "r"},
			wantFile: "https://huggingface.co/../../evil.example.com/blob/r/x",
			wantRepo: "https://huggingface.co/../../evil.example.com",
		},
		{
			name:     "an absolute-URL repo id cannot repoint the origin",
			p:        HFProvenance{Repo: "https://evil.example.com/x", Path: "f", Revision: "r"},
			wantFile: "https://huggingface.co/https://evil.example.com/x/blob/r/f",
			wantRepo: "https://huggingface.co/https://evil.example.com/x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FileURL(); got != tc.wantFile {
				t.Fatalf("FileURL() = %q, want %q", got, tc.wantFile)
			}
			if got := tc.p.RepoURL(); got != tc.wantRepo {
				t.Fatalf("RepoURL() = %q, want %q", got, tc.wantRepo)
			}
			// Whatever a row contains, the ORIGIN must always be huggingface.co over
			// https — the base is a constant, never anything read from the database.
			for _, raw := range []string{tc.p.FileURL(), tc.p.RepoURL()} {
				if raw == "" {
					continue
				}
				u, err := url.Parse(raw)
				if err != nil {
					t.Fatalf("built an unparseable URL %q: %v", raw, err)
				}
				if u.Scheme != "https" || u.Host != "huggingface.co" {
					t.Fatalf("URL %q resolved to %s://%s, want https://huggingface.co", raw, u.Scheme, u.Host)
				}
			}
		})
	}
}

// TestHFProvenanceHasNoConfidenceColumn is the structural enforcement of the
// decision recorded in claudedocs/HF-SOURCE-LINKING-PROPOSAL.md: only files WE
// downloaded are recorded, so there is exactly one kind of row. Re-introducing a
// confidence/tier column would re-open the "identical file somewhere" claim that
// a source link must never make.
func TestHFProvenanceHasNoConfidenceColumn(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.DB().Query(`PRAGMA table_info(hf_provenance)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := "sha256,repo,path,revision,recorded_at"
	if got := strings.Join(cols, ","); got != want {
		t.Fatalf("hf_provenance columns = %q, want exactly %q", got, want)
	}
}
