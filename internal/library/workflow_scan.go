package library

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Workflow-scan bounds. These are defensive limits on an arbitrary directory the
// web endpoint may point us at: a walk that hits maxWorkflowFiles *.json files
// aborts, and any single .json larger than maxWorkflowBytes is skipped (a graph
// is small — a huge/garbage file is not a workflow and must not be slurped into
// memory).
const (
	defaultMaxWorkflowFiles = 20_000
	maxWorkflowBytes        = 8 << 20 // 8 MiB per .json
)

// WorkflowScanStore is the store surface the workflow scanner needs. *store.Store
// satisfies it; tests may inject a stub.
type WorkflowScanStore interface {
	GetWorkflowByPath(ctx context.Context, path string) (*store.Workflow, error)
	UpsertWorkflowByPath(ctx context.Context, wf *store.Workflow) (id int64, updated bool, err error)
	FindVersionByFileName(ctx context.Context, basename string) (modelID, versionID *int, ok bool, err error)
}

// WorkflowResult is the per-file outcome streamed via WorkflowScanOptions.OnWorkflow
// as the scanner walks. Linked reports whether an auto-link resolved a civitai
// version; Updated reports an existing row was refreshed (vs. a fresh insert);
// Unchanged reports the incremental cache skipped a re-parse.
type WorkflowResult struct {
	Path      string
	Name      string
	Format    string
	Resources []string
	ModelID   *int
	VersionID *int
	Linked    bool
	Updated   bool
	Unchanged bool
}

// WorkflowScanReport summarizes a completed workflow scan.
type WorkflowScanReport struct {
	Dirs      []string
	Found     int // workflow .json files recorded (inserted or updated) this run
	Linked    int // of Found, how many auto-linked to a civitai version
	Unchanged int // cache hits (size+mtime unchanged) skipped without re-parsing
	Skipped   int // .json files that were not a comfy workflow
}

// WorkflowScanOptions configures a workflow scan.
type WorkflowScanOptions struct {
	// MaxFiles bounds how many *.json files the walk visits before aborting with
	// ErrScanTooLarge. 0 uses defaultMaxWorkflowFiles.
	MaxFiles int
	// OnWorkflow, when non-nil, STREAMS each processed workflow (after its row is
	// upserted). It is called sequentially from the single scan goroutine, so it
	// need not be concurrency-safe on its own — but the web layer still guards its
	// job state with a mutex.
	OnWorkflow func(WorkflowResult)
}

// WorkflowScanner walks ComfyUI workflow directories, parses each *.json as a
// comfy graph, auto-links it to a local civitai match by referenced filename, and
// upserts it as a source='scanned' workflow. It is separate from the model
// Scanner (a workflow is a graph, not a weight file — overloading the model
// scanner's classify() would be wrong).
type WorkflowScanner struct {
	store WorkflowScanStore
	log   *slog.Logger
}

// NewWorkflowScanner builds a WorkflowScanner. A nil logger discards output.
func NewWorkflowScanner(st WorkflowScanStore, log *slog.Logger) *WorkflowScanner {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &WorkflowScanner{store: st, log: log}
}

// ScanWorkflows walks each dir for *.json workflow graphs, records them, and
// returns a report. It is read-only on disk (it only reads files) and idempotent
// across re-runs (path-keyed upsert + the size/mtime incremental cache).
func (ws *WorkflowScanner) ScanWorkflows(ctx context.Context, dirs []string, opts WorkflowScanOptions) (*WorkflowScanReport, error) {
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxWorkflowFiles
	}
	report := &WorkflowScanReport{Dirs: dedupeStrings(dirs)}

	jsonFiles, err := ws.walk(ctx, report.Dirs, maxFiles)
	if err != nil {
		return report, err
	}
	for _, path := range jsonFiles {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		ws.process(ctx, path, opts, report)
	}
	return report, nil
}

// walk collects *.json paths under the given dirs, skipping hidden directories,
// bounded by maxFiles. It never follows symlinked directories (WalkDir reports a
// symlinked dir with a symlink type, so d.IsDir() is false).
func (ws *WorkflowScanner) walk(ctx context.Context, dirs []string, maxFiles int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, root := range dirs {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".json") {
				return nil
			}
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				abs = filepath.Clean(path)
			}
			if seen[abs] {
				return nil
			}
			seen[abs] = true
			if len(out) >= maxFiles {
				return ErrScanTooLarge
			}
			out = append(out, abs)
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// process handles one .json: cache-check, parse, auto-link, upsert, stream.
func (ws *WorkflowScanner) process(ctx context.Context, path string, opts WorkflowScanOptions, report *WorkflowScanReport) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return
	}
	if fi.Size() > maxWorkflowBytes {
		return // too large to be a workflow graph — skip defensively
	}

	// Incremental cache: an unchanged file (same path/size/mtime) needs no re-parse
	// — but ONLY once it has been linked. An unchanged-yet-unlinked workflow is
	// re-processed every scan so that a model installed AFTER the first scan gets
	// picked up (the file is byte-identical, so a pure size/mtime cache would leave
	// it unlinked forever). Re-parsing the handful of still-unlinked graphs is cheap.
	cached, err := ws.store.GetWorkflowByPath(ctx, path)
	if err != nil {
		ws.log.Warn("workflow scan: cache lookup failed", "path", path, "err", err)
	}
	if workflowCacheHit(cached, fi) && cached.VersionID != nil {
		report.Unchanged++
		if opts.OnWorkflow != nil {
			opts.OnWorkflow(WorkflowResult{
				Path: path, Name: cached.Name, Format: cached.Format,
				Resources: cached.Resources, ModelID: cached.ModelID,
				VersionID: cached.VersionID, Linked: cached.VersionID != nil,
				Unchanged: true,
			})
		}
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		ws.log.Warn("workflow scan: read failed", "path", path, "err", err)
		return
	}
	format, err := comfy.DetectFormat(raw)
	if err != nil {
		report.Skipped++
		return // not a comfy workflow
	}

	resources, _ := comfy.ExtractResourcesAny(format, raw)
	modelID, versionID, linked := ws.autoLink(ctx, format, raw, resources)

	mtime := fi.ModTime().UTC()
	name := workflowNameFromPath(path)
	wf := &store.Workflow{
		Name:       name,
		Format:     format,
		Graph:      string(raw),
		Source:     store.WorkflowSourceScanned,
		ModelID:    modelID,
		VersionID:  versionID,
		Resources:  resources,
		SourcePath: path,
		SizeBytes:  fi.Size(),
		Mtime:      &mtime,
	}
	_, updated, err := ws.store.UpsertWorkflowByPath(ctx, wf)
	if err != nil {
		ws.log.Warn("workflow scan: upsert failed", "path", path, "err", err)
		return
	}
	report.Found++
	if linked {
		report.Linked++
	}
	if opts.OnWorkflow != nil {
		opts.OnWorkflow(WorkflowResult{
			Path: path, Name: name, Format: format, Resources: resources,
			ModelID: modelID, VersionID: versionID, Linked: linked, Updated: updated,
		})
	}
}

// autoLink resolves a scanned workflow to a civitai version by matching a
// referenced filename against the local library. It tries the PRIMARY checkpoint
// first (the most meaningful anchor), then falls back to each referenced resource
// basename until one matches. Returns (nil,nil,false) when nothing links.
//
// NOTE: the schema attaches a workflow to a SINGLE version, so the first match
// wins — a multi-checkpoint or ambiguous graph links to whichever resource
// resolves first (checkpoint preferred).
func (ws *WorkflowScanner) autoLink(ctx context.Context, format string, graph json.RawMessage, resources []string) (*int, *int, bool) {
	var candidates []string
	if ckpt, ok := comfy.PrimaryCheckpoint(format, graph); ok {
		candidates = append(candidates, ckpt)
	}
	candidates = append(candidates, resources...)

	tried := map[string]bool{}
	for _, c := range candidates {
		// comfy.PathBase, not filepath.Base: every candidate is GRAPH-DERIVED (the
		// primary checkpoint and the extracted resources above), so a Windows-authored
		// `zimage\zit_sda_v1.safetensors` must fold to its filename here. filepath.Base
		// is a no-op for backslashes on Linux, and FindVersionByFileName gates on
		// strings.ToLower(filepath.Base(local_files.path)) — a HOST basename that never
		// contains a backslash — so an un-folded candidate can never match and the
		// workflow silently fails to auto-link to its CivitAI model/version.
		//
		// The store side's filepath.Base stays filepath.Base: that one IS a real path
		// on this host.
		base := comfy.PathBase(strings.TrimSpace(c))
		if base == "" || base == "." || tried[strings.ToLower(base)] {
			continue
		}
		tried[strings.ToLower(base)] = true
		modelID, versionID, ok, err := ws.store.FindVersionByFileName(ctx, base)
		if err != nil {
			ws.log.Warn("workflow scan: auto-link lookup failed", "file", base, "err", err)
			continue
		}
		if ok {
			return modelID, versionID, true
		}
	}
	return nil, nil, false
}

// workflowCacheHit reports whether a stored scanned workflow matches the file on
// disk unchanged (same source_path, size, and mtime) — mirroring Scanner.cacheHit.
func workflowCacheHit(cached *store.Workflow, fi os.FileInfo) bool {
	if cached == nil || cached.SourcePath == "" || cached.Mtime == nil {
		return false
	}
	if cached.SizeBytes != fi.Size() {
		return false
	}
	return cached.Mtime.Equal(fi.ModTime())
}

// workflowNameFromPath derives a display name from a workflow file path: its base
// name without the .json extension.
func workflowNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// dedupeStrings returns the input with blanks and duplicates removed, order
// preserved.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
