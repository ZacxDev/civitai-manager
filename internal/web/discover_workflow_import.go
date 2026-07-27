package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// Bounds for the Discover-import fetch + unzip. Workflows zips are KB to tens of
// MB in practice; these caps refuse a resource-exhaustion / zip-bomb payload
// while staying comfortably above any real workflow pack.
const (
	// maxWorkflowZipBytes bounds the COMPRESSED download read (the fetched .zip).
	maxWorkflowZipBytes = 128 << 20 // 128 MiB
	// maxWorkflowZipEntries bounds how many entries an archive may contain.
	maxWorkflowZipEntries = 2000
	// maxWorkflowEntryBytes bounds a single entry's UNCOMPRESSED size (enforced by a
	// limited copy, so a lying header cannot exceed it).
	maxWorkflowEntryBytes = 32 << 20 // 32 MiB
	// maxWorkflowZipTotalBytes bounds the CUMULATIVE uncompressed bytes read from one
	// archive.
	maxWorkflowZipTotalBytes = 256 << 20 // 256 MiB
)

// handleWorkflowDiscoverImport imports a CivitAI Workflows-type model into the
// local workflow library (Discover D2): it downloads the model's Archive zip(s),
// unzips them in-memory, and stores each contained ComfyUI workflow .json as a
// store.Workflow pre-linked to the source model/version. Re-importing is
// idempotent — a workflow whose canonical graph hash already exists is skipped.
//
// Security posture (order matters): the body is bounded and parsed, then CSRF is
// verified and the endpoint is loopback-gated BEFORE any egress — the import
// downloads a gated file from civitai.com with the user's token, so it must never
// be reachable by a non-local caller nor forgeable cross-site. The zip is only
// unzipped + JSON-parsed; archive contents are NEVER executed, and zip-bomb caps
// bound entry count, per-entry size, and cumulative size.
func (s *Server) handleWorkflowDiscoverImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	// CSRF BEFORE any egress.
	if !s.verifyCSRF(w, r) {
		return
	}
	// Loopback-gate: import triggers a token-authed download from civitai.com.
	if !s.gate(w) {
		return
	}

	isHX := r.Header.Get("HX-Request") == "true"

	modelID, err := strconv.Atoi(r.PathValue("modelId"))
	if err != nil || modelID <= 0 {
		http.Error(w, "bad model id", http.StatusBadRequest)
		return
	}

	// A gated download needs the user's CivitAI token; surface a clear, actionable
	// error rather than attempting a fetch that 401s.
	if strings.TrimSpace(s.cfg.Token) == "" {
		s.importRespond(w, r, modelID, isHX,
			"Configure your CivitAI token (in your config) to import workflows.", false, 0)
		return
	}

	m, _, err := s.cachedModelDetail(r.Context(), modelID)
	if err != nil || m == nil {
		s.importRespond(w, r, modelID, isHX,
			"Could not load that model from CivitAI.", false, 0)
		return
	}
	// Defence-in-depth: this endpoint imports Workflows-type models only. The UI
	// only shows the import control on Workflows models, but a hand-crafted
	// loopback+CSRF POST against another model type should be rejected with a
	// clear message rather than silently reaching "no archive found". Guard on a
	// populated Type so a cache entry that omits it doesn't false-reject.
	if m.Type != "" && !strings.EqualFold(m.Type, "Workflows") {
		s.importRespond(w, r, modelID, isHX,
			"That model is not a Workflows-type model, so it has no workflows to import.", false, 0)
		return
	}
	if len(m.ModelVersions) == 0 {
		s.importRespond(w, r, modelID, isHX, "That model has no versions to import.", false, 0)
		return
	}
	// The primary version is modelVersions[0] (the creator's index order — the
	// version the detail page defaults to). Import its Archive file(s).
	version := m.ModelVersions[0]
	archives := archiveFiles(version.Files)
	if len(archives) == 0 {
		s.importRespond(w, r, modelID, isHX,
			"No workflow archive (.zip) found on that model's primary version.", false, 0)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	dl := s.downloader()
	var total workflowImportCounts
	for i := range archives {
		counts, ierr := s.importOneArchive(ctx, dl, m, &version, &archives[i])
		total.add(counts)
		if ierr != nil {
			// A hard failure (download / invalid zip / zip-bomb cap) stops the import.
			// Any workflows already stored from earlier archives are kept; report both.
			msg := ierr.Error()
			if total.imported > 0 || total.present > 0 {
				msg = total.summary() + " — then failed: " + ierr.Error()
			}
			s.importRespond(w, r, modelID, isHX, msg, false, total.singleImportedID())
			return
		}
	}

	s.importRespond(w, r, modelID, isHX, total.summary(), true, total.singleImportedID())
}

// workflowImportCounts tallies an import's outcome across archives/entries.
type workflowImportCounts struct {
	imported int     // newly stored
	present  int     // skipped — graph hash already in the library
	skipped  int     // skipped — non-json / non-workflow / unparseable entry
	ids      []int64 // ids of the newly-stored workflows (len == imported)
}

func (c *workflowImportCounts) add(o workflowImportCounts) {
	c.imported += o.imported
	c.present += o.present
	c.skipped += o.skipped
	c.ids = append(c.ids, o.ids...)
}

// singleImportedID returns the id of the one workflow imported this request, or 0
// when zero or more than one were imported — used to decide whether the result
// link deep-links to that workflow's page vs the Workflows library tab.
func (c workflowImportCounts) singleImportedID() int64 {
	if len(c.ids) == 1 {
		return c.ids[0]
	}
	return 0
}

// summary renders the human-readable import result line.
func (c workflowImportCounts) summary() string {
	return fmt.Sprintf("Imported %d workflow(s), %d already present, %d skipped.",
		c.imported, c.present, c.skipped)
}

// archiveFiles returns every Archive-type file of a version (a version may ship
// several distinct zips). Falls back to nothing when none are Archive-type — the
// caller reports "no archive found" rather than downloading model weights.
func archiveFiles(files []civitai.ModelVersionFile) []civitai.ModelVersionFile {
	var out []civitai.ModelVersionFile
	for _, f := range files {
		if strings.EqualFold(strings.TrimSpace(f.Type), "Archive") && strings.TrimSpace(f.DownloadURL) != "" {
			out = append(out, f)
		}
	}
	return out
}

// importOneArchive downloads one Archive file, unzips it in-memory, and stores
// each contained workflow .json. It returns the per-archive counts and a hard
// error (download failure, invalid zip, or a zip-bomb cap breach) that aborts the
// whole import; graceful per-entry issues (non-json / unparseable / unknown
// format) are counted as skipped, never errors.
func (s *Server) importOneArchive(ctx context.Context, dl civitai.Downloader, m *civitai.ModelDetail, version *civitai.ModelVersionSummary, file *civitai.ModelVersionFile) (workflowImportCounts, error) {
	var counts workflowImportCounts

	data, err := fetchBounded(ctx, dl, file.DownloadURL)
	if err != nil {
		return counts, err
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return counts, fmt.Errorf("archive is not a valid zip: %w", err)
	}
	if len(zr.File) > maxWorkflowZipEntries {
		return counts, fmt.Errorf("archive has too many entries (%d, cap %d)", len(zr.File), maxWorkflowZipEntries)
	}

	var totalUncompressed int64
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		// Only .json entries are workflow candidates; anything else (images, readme,
		// __MACOSX cruft) is skipped gracefully.
		if !strings.EqualFold(path.Ext(zf.Name), ".json") {
			counts.skipped++
			continue
		}

		raw, n, rerr := readZipEntry(zf, maxWorkflowEntryBytes)
		if rerr != nil {
			return counts, rerr // entry over the per-entry cap — refuse, stop.
		}
		totalUncompressed += n
		if totalUncompressed > maxWorkflowZipTotalBytes {
			return counts, fmt.Errorf("archive uncompressed size exceeds cap (%d bytes)", maxWorkflowZipTotalBytes)
		}

		format, ferr := comfy.DetectFormat(raw)
		if ferr != nil {
			// Not a recognizable ComfyUI graph — a non-workflow / unparseable json.
			counts.skipped++
			continue
		}

		hash := store.GraphHash(string(raw))
		exists, eerr := s.store.WorkflowExistsByGraphHash(ctx, hash)
		if eerr != nil {
			return counts, fmt.Errorf("dedup check: %w", eerr)
		}
		if exists {
			counts.present++
			continue
		}

		res, _ := comfy.ExtractResourcesAny(format, raw)
		modelID := m.ID
		versionID := version.ID
		wf := &store.Workflow{
			Name:      defaultWorkflowName(zf.Name),
			Format:    format,
			Graph:     string(raw),
			GraphHash: hash,
			Source:    store.WorkflowSourceCivitai,
			ModelID:   &modelID,
			VersionID: &versionID,
			BaseModel: version.BaseModel,
			Resources: res,
		}
		id, ierr := s.store.InsertWorkflow(ctx, wf)
		if ierr != nil {
			return counts, fmt.Errorf("store workflow: %w", ierr)
		}
		counts.ids = append(counts.ids, id)
		counts.imported++
	}
	return counts, nil
}

// fetchBounded downloads a URL through the CivitAI downloader (which resolves
// gated-file auth from the configured token) and reads at most maxWorkflowZipBytes
// into memory, erroring if the body exceeds that cap or the server returns a
// non-2xx status.
func fetchBounded(ctx context.Context, dl civitai.Downloader, url string) ([]byte, error) {
	resp, err := dl.DownloadFile(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download failed: server returned %s", resp.Status)
	}
	// Read one byte past the cap so we can distinguish "exactly at cap" from "over".
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkflowZipBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read download: %w", err)
	}
	if len(data) > maxWorkflowZipBytes {
		return nil, fmt.Errorf("archive exceeds the %d-byte download cap", maxWorkflowZipBytes)
	}
	return data, nil
}

// readZipEntry reads a single zip entry's decompressed bytes, refusing to read
// more than cap bytes (a limited copy, so a lying uncompressed-size header cannot
// exceed the cap or exhaust memory). Returns the bytes and the count read.
func readZipEntry(zf *zip.File, maxBytes int64) ([]byte, int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, 0, fmt.Errorf("open zip entry %q: %w", zf.Name, err)
	}
	defer rc.Close()
	// Read maxBytes+1 so an entry exactly at the cap is allowed but one over is refused.
	limited := io.LimitReader(rc, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, 0, fmt.Errorf("read zip entry %q: %w", zf.Name, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, 0, fmt.Errorf("zip entry %q exceeds the per-entry size cap (%d bytes)", zf.Name, maxBytes)
	}
	return data, int64(len(data)), nil
}

// importRespond renders the import outcome: an inline result fragment for an htmx
// POST (swapped into the button's own container), or a POST-redirect-GET to the
// Workflows library tab with a flash for a plain form POST.
func (s *Server) importRespond(w http.ResponseWriter, r *http.Request, modelID int, isHX bool, msg string, ok bool, workflowID int64) {
	if isHX {
		s.render(w, http.StatusOK, workflowImportResult(modelID, msg, ok, workflowID))
		return
	}
	level := "success"
	if !ok {
		level = "error"
	}
	s.redirectWorkflows(w, r, msg, level)
}

// workflowImportContainerID is the stable element the import button and its result
// share, so the POST can swap the button out for the outcome inline.
func workflowImportContainerID(modelID int) string {
	return fmt.Sprintf("wf-import-%d", modelID)
}

// workflowImportAction renders the "Import workflow(s)" affordance for a Workflows
// model (used on both the discover cards and the model detail page). The button
// POSTs the import endpoint (CSRF via hx-vals) and swaps its own container with
// the result. A short note makes the civitai.com egress explicit.
func workflowImportAction(modelID int, csrf string) g.Node {
	id := workflowImportContainerID(modelID)
	return h.Div(
		h.ID(id),
		h.Class("flex flex-col gap-1"),
		civButton("filled", "sm", []g.Node{
			h.Type("button"),
			hx("post", fmt.Sprintf("/workflows/discover/%d/import", modelID)),
			hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
			hx("target", "#"+id),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "this"),
		}, g.Text("Import workflow(s)")),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Downloads the workflow zip from civitai.com using your token, then stores each workflow locally.")),
	)
}

// workflowImportResult renders the inline outcome that replaces the import button
// after the POST: the counts line (green on success, amber on failure) plus a
// "View in library" link. When EXACTLY ONE workflow was imported this request the
// link deep-links to that workflow's page (/workflows/{id}); for zero or more than
// one it falls back to the Workflows library tab.
func workflowImportResult(modelID int, msg string, ok bool, workflowID int64) g.Node {
	cls := "text-xs text-amber-400"
	if ok {
		cls = "text-xs font-medium cm-ok"
	}
	href := "/library?tab=workflows"
	if workflowID > 0 {
		href = fmt.Sprintf("/workflows/%d", workflowID)
	}
	return g.Group([]g.Node{
		h.P(h.Class(cls), g.Text(msg)),
		h.A(
			h.Href(href),
			h.Class("text-xs text-indigo-400 hover:underline"),
			g.Text("View in library →"),
		),
	})
}
