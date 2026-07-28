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
// root is (still) an existing directory.
//
// This runs on EVERY run-status poll (~1-2s during a run), so it must stay cheap:
// it does an os.Stat (exists + is-dir) re-check only — NOT a CreateTemp
// write-probe, which would churn a temp file in the ComfyUI models dir on every
// poll. Writability was validated at config load; if the dir became read-only
// since, the actual download (comfy.WriteModelStream) fails cleanly and the user
// sees the error — button visibility does not need the stronger probe.
func (s *Server) comfyDownloadEligible() bool {
	root := strings.TrimSpace(s.cfg.ComfyModelPath)
	if root == "" {
		return false
	}
	if !comfyURLIsLocal(s.cfg.ComfyURL) {
		return false
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		if err != nil {
			s.log.Warn("comfy_model_path no longer available", "path", root, "err", err)
		}
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
	// ExpectedSHA256, when set, is verified against the streamed bytes before the
	// atomic rename (the HuggingFace path pins on the tree's LFS oid). Empty for the
	// civitai path (no hash to pin).
	ExpectedSHA256 string
	// SourceHF routes the fetch through the SSRF-hardened HuggingFace client instead
	// of the civitai downloader.
	SourceHF bool
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
	if !s.comfyDownloadEligible() {
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

	// Fast path: for a routable CivitAI type whose destination already exists, skip
	// the network entirely and just run the original workflow.
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); derr == nil && fileExists(dest) {
			s.startRun(wf, runOptions{})
			s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
			return
		}
	}

	// Resolve the install source: CivitAI first, then the HuggingFace fallback (only
	// an auto-eligible HF match — curated/recognized-org + non-gated + exact + sha).
	// Ambiguous/zero → show the resolve cards (no download).
	plan, ok := s.resolveInstallPlan(r.Context(), filename, typ, chosenModel)
	if !ok {
		s.renderResolveFallback(w, r, filename, typ)
		return
	}

	// Already installed at the resolved destination → skip the download, run.
	if fileExists(plan.DestPath) {
		s.startRun(wf, runOptions{})
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}

	pd := pendingDownload{
		FileName:          plan.FileName,
		URL:               plan.URL,
		DestPath:          plan.DestPath,
		MaxBytes:          s.cfg.MaxFileSizeBytes,
		ContentLengthHint: plan.ContentLengthHint,
		ExpectedSHA256:    plan.ExpectedSHA256,
		SourceHF:          plan.SourceHF,
	}
	s.startDownloadAndRun(wf, pd, runOptions{})
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true, s.nsfwMode()))
}

// handleWorkflowInstallOptionAndRun installs the model FILE behind ONE model-file
// incompatible-option (resolve → download into the local ComfyUI models dir) AND
// applies the user's picked fixes for the OTHER options in the section, then runs the
// ORIGINAL workflow ephemerally — the stored workflow is never mutated. It is
// CSRF-protected + loopback-gated (same prologue as handleWorkflowDownloadAndRun) and
// reaches civitai.com/HuggingFace (egress) + the local filesystem + ComfyUI.
//
// The section form hx-includes carry the whole group's opt_input/opt_old/opt_new
// arrays; parseOptionFixes turns the picked ones into fixes. The install target's own
// fix is DROPPED (we install the exact file, not substitute it). It degrades safely
// exactly like download-and-run: not eligible / unroutable / ambiguous → writes
// NOTHING and returns the resolve/link fragment.
func (s *Server) handleWorkflowInstallOptionAndRun(w http.ResponseWriter, r *http.Request) {
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
	filename := strings.TrimSpace(r.FormValue("install_filename"))
	if filename == "" {
		http.Error(w, "missing install_filename", http.StatusBadRequest)
		return
	}
	typ := civitaiTypeParam(r.FormValue("install_type"))

	// Fixes for the OTHER groups (the picks). Drop any fix keyed on the install
	// target's own value — installing the exact file makes the original value valid,
	// so it must NOT also be rewritten to a substitute.
	fixes := parseOptionFixes(r.Form)
	for k := range fixes {
		if k.OldValue == filename {
			delete(fixes, k)
		}
	}
	opts := runOptions{OptionFixes: fixes}

	// Not eligible → link-only fallback (never attempt a write/download).
	if !s.comfyDownloadEligible() {
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

	// Fast path: a routable CivitAI type whose destination already exists → skip the
	// network and run with the picked option-fixes (the original value is valid).
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, derr := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); derr == nil && fileExists(dest) {
			s.startRun(wf, opts)
			s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
			return
		}
	}

	// Resolve the install source (CivitAI first, then the auto-eligible HF fallback).
	// A bad-option install never disambiguates to a chosen model (chosenModel = 0), so
	// the detector's HF curated path is reachable. Ambiguous/zero → resolve cards.
	plan, ok := s.resolveInstallPlan(r.Context(), filename, typ, 0)
	if !ok {
		s.renderResolveFallback(w, r, filename, typ)
		return
	}

	// Already installed at the resolved destination → skip the download, run with fixes.
	if fileExists(plan.DestPath) {
		s.startRun(wf, opts)
		s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
		return
	}

	pd := pendingDownload{
		FileName:          plan.FileName,
		URL:               plan.URL,
		DestPath:          plan.DestPath,
		MaxBytes:          s.cfg.MaxFileSizeBytes,
		ContentLengthHint: plan.ContentLengthHint,
		ExpectedSHA256:    plan.ExpectedSHA256,
		SourceHF:          plan.SourceHF,
	}
	s.startDownloadAndRun(wf, pd, opts)
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, true, s.nsfwMode()))
}

// installPlan is a resolved, ready-to-execute install: the source download URL, the
// containment-checked destination under comfy_model_path, and — for a HuggingFace
// source — the expected sha256 the bytes are verified against before the rename.
type installPlan struct {
	FileName          string
	URL               string
	DestPath          string
	ContentLengthHint int64
	// ExpectedSHA256 is the HF file's LFS oid; the download is refused unless the
	// streamed bytes hash to it. Empty for the civitai source (no hash to pin).
	ExpectedSHA256 string
	// SourceHF routes the download through the hardened HuggingFace client.
	SourceHF bool
}

// resolveInstallPlan resolves a missing FILENAME to an install plan. It tries CivitAI
// first (only for a routable type, whose subdir gives the destination) and, on a
// CivitAI miss, the HuggingFace fallback — but ONLY an auto-download-eligible HF match
// (curated-map/recognized-org + non-gated + exact-basename + a captured sha256 + a
// determinable ComfyUI subdir). An explicitly chosen CivitAI model (model_id) is
// CivitAI-only and never falls back to HF. Returns ok=false when nothing installable
// resolves (the caller then shows the resolve cards).
func (s *Server) resolveInstallPlan(ctx context.Context, filename, typ string, chosenModel int) (installPlan, bool) {
	// CivitAI branch — needs a routable type so the destination subdir is defined.
	if subdir, ok := comfy.TypeSubdir(typ); ok {
		if dest, err := comfy.SafeModelDest(s.cfg.ComfyModelPath, subdir, filename); err == nil {
			if src, ok := s.resolveDownloadSource(ctx, filename, typ, chosenModel); ok {
				return installPlan{
					FileName:          path.Base(strings.ReplaceAll(filename, "\\", "/")),
					URL:               src.DownloadURL,
					DestPath:          dest,
					ContentLengthHint: src.SizeBytes,
				}, true
			}
		}
	}

	// HuggingFace fallback — never for an explicitly chosen CivitAI model.
	if chosenModel == 0 {
		if m := s.resolveHF(ctx, filename); m != nil && s.hfInstallEligible(m) {
			if dest, err := comfy.SafeModelDest(s.cfg.ComfyModelPath, m.Subdir, m.FileName); err == nil {
				return installPlan{
					FileName:       m.FileName,
					URL:            m.URL,
					DestPath:       dest,
					ExpectedSHA256: m.SHA256,
					SourceHF:       true,
				}, true
			}
		}
	}
	return installPlan{}, false
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
//
// Supply-chain note: a workflow's model reference carries only a filename, not a
// hash, so the resolved file cannot be pinned/verified against an expected digest.
// "Download & run" therefore TRUSTS CivitAI's search ranking + the basename match
// to identify the intended model. The download itself is still constrained by the
// SDK's HTTPS-only, private-IP-blocking, civitai-host-scoped-token dialer, and by
// the size cap and path-containment guards — but the CHOICE of file is a trust in
// CivitAI, same as every other download in the app.
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
// the workflow with opts. It respects the one-run-at-a-time invariant (a click while
// any run/download is in flight is a no-op).
//
// opts is EMPTY for the plain download-and-run (the referenced file now exists on
// disk, so the original graph resolves unchanged). For install-option-and-run it
// carries the OTHER incompatible-options' picked fixes, applied to the ephemeral graph
// after the file lands — so the whole section resolves in one action. The stored
// workflow is never touched.
func (s *Server) startDownloadAndRun(wf *store.Workflow, pd pendingDownload, opts runOptions) {
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
	download := s.downloadFn
	if download == nil {
		download = s.downloadModelFile
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
			err = download(ctx, pd, func(msg string) {
				up.setPhase(runPhaseDownloading, msg, 0)
			})
			if err != nil {
				return
			}
			res, err = run(ctx, wf, up, opts)
		}()
		// Settle + capture through the SHARED tail so a successful download-and-run
		// lands in the output gallery exactly like a plain run. This goroutine used to
		// take runMu itself (with a deferred unlock) around applyRunOutcomeLocked;
		// settleAndCapture now owns that lock/unlock, because it must RELEASE runMu
		// before the capture, which does network + disk work off the run mutex. (The
		// enclosing startDownloadAndRun still holds runMu across its own body — that
		// is the one-run-at-a-time guard and is unrelated to this goroutine.)
		s.settleAndCapture(job, wf, opts, res, err)
	}()
}

// downloadModelFile fetches pd.URL and writes it atomically to pd.DestPath under
// the ComfyUI models dir, enforcing the size cap and streaming throttled progress
// via cb. An already-present destination (ErrDestExists) is NOT an error — the
// file is there, so the run can proceed.
func (s *Server) downloadModelFile(ctx context.Context, pd pendingDownload, cb func(string)) error {
	cb("Downloading " + pd.FileName + "…")
	if err := assertHTTPSDownloadURL(pd.URL); err != nil {
		return err
	}
	resp, err := s.modelDownloader(pd).DownloadFile(ctx, pd.URL)
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
	// The HuggingFace path pins on a known sha256 (the tree's LFS oid); the civitai
	// path has no hash to pin (empty → verification skipped, unchanged behavior).
	_, err = comfy.WriteModelStreamVerified(pd.DestPath, pr, contentLen, pd.MaxBytes, pd.ExpectedSHA256)
	if errors.Is(err, comfy.ErrDestExists) {
		return nil // installed concurrently / already present — proceed to run
	}
	if err != nil {
		return err
	}
	cb("Download complete — starting run…")
	return nil
}

// assertHTTPSDownloadURL is a cheap, app-level belt on every API-supplied download
// URL (CivitAI `downloadUrl`, HF resolve URL) before it is handed to a downloader:
// the scheme MUST be https, else we refuse WITHOUT egressing. This catches an
// http:/file:/other-scheme URL from a malicious or compromised API response at the
// app layer. It is intentionally NOT a host allowlist — that risks breaking legit
// downloads for marginal gain.
//
// The private-IP / SSRF dial-time block (and token host-scoping) remains the SDK's
// job: the `github.com/civitai/cli` downloader's dialer blocks private/link-local
// dial targets and scopes the bearer token to civitai hosts (covered by its
// download_ssrf_test.go — IsBlockedDownloadIP / RequireHTTPSDownload / loopback
// refusal; at the pinned v0.1.82 that lives in pkg/civitai/). RE-VERIFY that guard
// on any civitai/cli bump — this app-level assertion does not replace it.
func assertHTTPSDownloadURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("refusing download: unparseable URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("refusing download: URL scheme %q is not https", u.Scheme)
	}
	return nil
}

// modelDownloader picks the fetcher for a pending download: the SSRF-hardened
// HuggingFace client for an HF source, otherwise the civitai downloader. Both
// satisfy the same (ctx,url)→(*http.Response,error) shape.
func (s *Server) modelDownloader(pd pendingDownload) interface {
	DownloadFile(ctx context.Context, fileURL string) (*http.Response, error)
} {
	if pd.SourceHF {
		if c := s.hfClientOrNil(); c != nil {
			return c
		}
	}
	return s.downloader()
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

