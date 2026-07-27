package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// comfyURLIsLocal reports whether the configured ComfyUI base URL points at the
// loopback interface — the precondition for writing into its local models dir.
// A non-loopback ComfyUI is on another host, so we cannot install files for it.
func comfyURLIsLocal(comfyURL string) bool {
	u, err := url.Parse(strings.TrimSpace(comfyURL))
	if err != nil || u.Host == "" {
		return false
	}
	return config.IsLoopbackAddr(u.Host)
}

// comfyDownloadEligible reports whether the "Download & run" action is available:
// comfy_model_path is configured, the ComfyUI is local (loopback), and the model
// root is (still) an existing writable directory. Config load already validated
// writability, but we re-check at request time because the directory could have
// been removed/remounted since startup (Memory-is-a-hypothesis).
func (s *Server) comfyDownloadEligible() bool {
	root := strings.TrimSpace(s.cfg.ComfyModelPath)
	if root == "" {
		return false
	}
	if !comfyURLIsLocal(s.cfg.ComfyURL) {
		return false
	}
	if err := config.ValidateWritableDir(root); err != nil {
		s.log.Warn("comfy_model_path no longer writable", "path", root, "err", err)
		return false
	}
	return true
}

// comfyTypeRoutable reports whether a CivitAI type maps to a known ComfyUI
// subfolder (so a download for it has a well-defined destination).
func comfyTypeRoutable(civitaiType string) bool {
	_, ok := comfy.TypeSubdir(civitaiType)
	return ok
}

// pendingDownload is the resolved plan for a Download & run: the file's civitai
// download URL, the destination path under comfy_model_path, a size cap, and the
// advertised size hint (for the free-disk pre-check and progress). It carries the
// bare basename for display.
type pendingDownload struct {
	FileName          string
	URL               string
	DestPath          string
	MaxBytes          int64
	ContentLengthHint int64
}

// resolvedDownload is one file candidate parsed from a model-search raw body: a
// model version's file, with the parent model id (for the deep link) and its
// download URL / size. Only files with a non-empty download URL are usable.
type resolvedDownload struct {
	ModelID     int
	FileName    string
	DownloadURL string
	SizeBytes   int64
}

// handleWorkflowDownloadAndRun resolves a missing model FILENAME to a CivitAI
// file, downloads it into the local ComfyUI models dir under the exact reference
// name, then auto-runs the ORIGINAL workflow (which now finds the file). It is
// CSRF-protected + loopback-gated (same prologue order as
// handleWorkflowRunSubstitute: ParseForm → verifyCSRF → gate) and reaches both
// civitai.com (egress) and the local filesystem.
//
// It degrades safely: when the flow is not eligible (comfy_model_path unset /
// non-writable, or a non-local ComfyUI), or the type is unroutable, or resolution
// is ambiguous/zero, it writes NOTHING and returns the CivitAI-link/resolve
// fragment so the user can pick a model manually.
func (s *Server) handleWorkflowDownloadAndRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	filename := strings.TrimSpace(r.FormValue("filename"))
	if filename == "" {
		http.Error(w, "missing filename", http.StatusBadRequest)
		return
	}
	typ := civitaiTypeParam(r.FormValue("type"))
	// An optionally chosen model (from a disambiguation click) narrows resolution to
	// exactly that model.
	chosenModel, _ := strconv.Atoi(r.FormValue("model_id"))

	// Not eligible → link-only fallback (never attempt a write/download).
	if !s.comfyDownloadEligible() || !comfyTypeRoutable(typ) {
		s.renderResolveFallback(w, r, filename, typ)
		return
	}

	subdir, _ := comfy.TypeSubdir(typ) // ok guaranteed by comfyTypeRoutable
	dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename)
	if derr != nil {
		s.renderResolveFallback(w, r, filename, typ)
		return
	}

	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}

	// Already installed → skip the download, just run the original workflow.
	if fileExists(dest) {
		s.startRun(wf, runOptions{})
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible()))
		return
	}

	// Resolve the download source. Ambiguous/zero → show the resolve cards (no
	// download), so the user can pick a specific model to install.
	src, ok := s.resolveDownloadSource(r.Context(), filename, typ, chosenModel)
	if !ok {
		s.renderResolveFallback(w, r, filename, typ)
		return
	}

	pd := pendingDownload{
		FileName:          path.Base(strings.ReplaceAll(filename, "\\", "/")),
		URL:               src.DownloadURL,
		DestPath:          dest,
		MaxBytes:          s.cfg.MaxFileSizeBytes,
		ContentLengthHint: src.SizeBytes,
	}
	s.startDownloadAndRun(wf, pd)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true))
}

// renderResolveFallback renders the existing resolve fragment (heuristic model
// cards + "Search CivitAI" link) for a filename — the degrade path when a
// download cannot/should not proceed automatically.
func (s *Server) renderResolveFallback(w http.ResponseWriter, r *http.Request, filename, typ string) {
	query := comfy.CleanModelQuery(filename)
	var res *civitai.ModelSearchResult
	if query != "" {
		res = s.resolveModels(r.Context(), query, typ)
	}
	s.render(w, http.StatusOK, resolveModelFragment(query, res, s.nsfwMode()))
}

// fileExists reports whether path exists (any file type). Used to skip a download
// when the referenced model is already installed.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveDownloadSource resolves a missing FILENAME to a downloadable CivitAI
// file. When chosenModel>0 it fetches that model directly and picks the file
// whose basename equals filename (else the primary file of the primary version).
// Otherwise it searches (types PLURAL, TTL-cached) and looks for the UNIQUE file
// across results whose basename equals filename — the strong, unambiguous signal.
// It returns ok=false when nothing matches (the caller then shows the resolve
// cards instead of auto-downloading).
func (s *Server) resolveDownloadSource(parent context.Context, filename, typ string, chosenModel int) (resolvedDownload, bool) {
	want := path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))

	if chosenModel > 0 {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		_, raw, err := s.reader.GetModel(ctx, strconv.Itoa(chosenModel))
		if err != nil {
			s.log.Warn("download-and-run: GetModel failed", "model", chosenModel, "err", err)
			return resolvedDownload{}, false
		}
		return pickFileFromModelRaw(raw, want)
	}

	query := comfy.CleanModelQuery(filename)
	if query == "" {
		return resolvedDownload{}, false
	}
	res := s.resolveModels(parent, query, typ)
	if res == nil || len(res.Raw) == 0 {
		return resolvedDownload{}, false
	}
	return pickFileFromSearchRaw(res.Raw, want)
}

// searchFileEnvelope decodes the file-bearing subset of a model-search /
// model-detail raw body. Both the models-list and models/{id} responses embed
// modelVersions[].files[] with name/downloadUrl/sizeKB.
type searchFileEnvelope struct {
	Items []modelWithVersions `json:"items"`
}

type modelDetailEnvelope struct {
	modelWithVersions
}

type modelWithVersions struct {
	ID            int `json:"id"`
	ModelVersions []struct {
		Files []struct {
			Name        string  `json:"name"`
			DownloadURL string  `json:"downloadUrl"`
			SizeKB      float64 `json:"sizeKB"`
			Primary     bool    `json:"primary"`
		} `json:"files"`
	} `json:"modelVersions"`
}

// pickFileFromSearchRaw finds, across all models/versions in a search body, the
// file whose basename equals want (case-insensitive). It returns the FIRST such
// match with a non-empty download URL — an exact-filename hit is the unambiguous
// "this is the file the workflow references" signal.
func pickFileFromSearchRaw(raw []byte, want string) (resolvedDownload, bool) {
	var body searchFileEnvelope
	if err := json.Unmarshal(raw, &body); err != nil {
		return resolvedDownload{}, false
	}
	for _, m := range body.Items {
		if rd, ok := m.pickFile(want); ok {
			return rd, true
		}
	}
	return resolvedDownload{}, false
}

// pickFileFromModelRaw finds, in a single model's detail body, the file whose
// basename equals want; failing that it falls back to the primary file of the
// primary (positional [0]) version — the version the detail page defaults to.
func pickFileFromModelRaw(raw []byte, want string) (resolvedDownload, bool) {
	var body modelDetailEnvelope
	if err := json.Unmarshal(raw, &body); err != nil {
		return resolvedDownload{}, false
	}
	if rd, ok := body.pickFile(want); ok {
		return rd, true
	}
	// Fallback: primary file of the primary version.
	for _, v := range body.ModelVersions {
		var first *resolvedDownload
		for _, f := range v.Files {
			if strings.TrimSpace(f.DownloadURL) == "" {
				continue
			}
			rd := resolvedDownload{ModelID: body.ID, FileName: f.Name, DownloadURL: f.DownloadURL, SizeBytes: int64(f.SizeKB * 1024)}
			if f.Primary {
				return rd, true
			}
			if first == nil {
				cp := rd
				first = &cp
			}
		}
		if first != nil {
			return *first, true
		}
	}
	return resolvedDownload{}, false
}

// pickFile returns this model's file whose basename equals want (case-insensitive)
// and has a non-empty download URL.
func (m modelWithVersions) pickFile(want string) (resolvedDownload, bool) {
	lowWant := strings.ToLower(want)
	for _, v := range m.ModelVersions {
		for _, f := range v.Files {
			if strings.TrimSpace(f.DownloadURL) == "" {
				continue
			}
			base := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
			if strings.ToLower(base) == lowWant {
				return resolvedDownload{
					ModelID:     m.ID,
					FileName:    f.Name,
					DownloadURL: f.DownloadURL,
					SizeBytes:   int64(f.SizeKB * 1024),
				}, true
			}
		}
	}
	return resolvedDownload{}, false
}

// startDownloadAndRun launches a background job that FIRST downloads pd into the
// ComfyUI models dir (streaming progress into the run-status container), then runs
// the workflow normally. It respects the one-run-at-a-time invariant (a click
// while any run/download is in flight is a no-op). The run uses EMPTY runOptions —
// no substitution — because the referenced file now exists on disk, so the
// original graph resolves unchanged.
func (s *Server) startDownloadAndRun(wf *store.Workflow, pd pendingDownload) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runJob != nil && s.runJob.running {
		return // one run at a time
	}

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, runJobBudget)
	job := &runJob{
		running: true, workflowID: wf.ID, phase: runPhaseDownloading,
		message: "Preparing download…", startedAt: time.Now(), cancel: cancel,
	}
	s.runJob = job

	up := s.newRunUpdater(job)
	run := s.runFn
	if run == nil {
		run = s.realRun
	}

	go func() {
		defer cancel()
		var res *runResult
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("download-and-run panicked: %v", r)
				}
			}()
			err = s.downloadModelFile(ctx, pd, func(msg string) {
				up.setPhase(runPhaseDownloading, msg, 0)
			})
			if err != nil {
				return
			}
			res, err = run(ctx, wf, up, runOptions{})
		}()
		s.runMu.Lock()
		defer s.runMu.Unlock()
		s.applyRunOutcomeLocked(job, res, err)
	}()
}

// downloadModelFile fetches pd.URL and writes it atomically to pd.DestPath under
// the ComfyUI models dir, enforcing the size cap and streaming throttled progress
// via cb. An already-present destination (ErrDestExists) is NOT an error — the
// file is there, so the run can proceed.
func (s *Server) downloadModelFile(ctx context.Context, pd pendingDownload, cb func(string)) error {
	cb("Downloading " + pd.FileName + "…")
	resp, err := s.downloader().DownloadFile(ctx, pd.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", pd.FileName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		snippet := make([]byte, 256)
		n, _ := io.ReadFull(io.LimitReader(resp.Body, 256), snippet)
		return fmt.Errorf("download %s returned HTTP %d: %s", pd.FileName, resp.StatusCode, string(snippet[:n]))
	}

	contentLen := resp.ContentLength
	if contentLen <= 0 && pd.ContentLengthHint > 0 {
		contentLen = pd.ContentLengthHint
	}
	pr := &downloadProgressReader{
		r: resp.Body, total: contentLen, name: pd.FileName, cb: cb, last: time.Now(),
	}
	_, err = comfy.WriteModelStream(pd.DestPath, pr, contentLen, pd.MaxBytes)
	if errors.Is(err, comfy.ErrDestExists) {
		return nil // installed concurrently / already present — proceed to run
	}
	if err != nil {
		return err
	}
	cb("Download complete — starting run…")
	return nil
}

// downloadProgressReader wraps a download body and reports throttled progress
// (at most every progressInterval) through cb, so the run-status poller shows a
// moving "Downloading… N%" line during an otherwise-silent multi-second fetch.
type downloadProgressReader struct {
	r     io.Reader
	total int64
	name  string
	read  int64
	last  time.Time
	cb    func(string)
}

const progressInterval = 500 * time.Millisecond

func (d *downloadProgressReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.read += int64(n)
	if time.Since(d.last) >= progressInterval {
		d.last = time.Now()
		d.cb(d.message())
	}
	return n, err
}

func (d *downloadProgressReader) message() string {
	if d.total > 0 {
		pct := d.read * 100 / d.total
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("Downloading %s… %d%% (%s / %s)", d.name, pct, humanBytes(d.read), humanBytes(d.total))
	}
	return fmt.Sprintf("Downloading %s… %s", d.name, humanBytes(d.read))
}

// downloadAndRunButton is the per-missing-model "Download & run" control. It POSTs
// the missing filename + its inferred CivitAI type to the download-and-run
// endpoint and swaps the run-status container (download phase → run phase). Shown
// only when the flow is eligible (see missingModelRow). The CSRF token, filename,
// and type travel in hx-vals as JSON-escaped values.
func downloadAndRunButton(mm comfy.MissingModel, wfID int64, csrf string) g.Node {
	id := strconv.FormatInt(wfID, 10)
	b, _ := json.Marshal(map[string]string{
		"csrf_token": csrf,
		"filename":   mm.Filename,
		"type":       mm.CivitaiType,
	})
	return civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/download-and-run"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("vals", string(b)),
	}, g.Text("Download & run"))
}
