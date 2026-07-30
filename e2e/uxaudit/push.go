package uxaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// This file mirrors auditloop's plugin-push contract
// (auditloop/internal/plugin/schema.go + upload.go). civitai-manager is a
// different Go module and CANNOT import auditloop, so the wire shape is
// re-declared here (kept byte-compatible with the server's DisallowUnknownFields
// decoder). The push endpoint + constraints:
//
//	POST {baseURL}/api/plugins/runs
//	Authorization: Bearer <token>
//	multipart/form-data: a `metadata` field (PushPayload JSON) + one file part per
//	referenced artifact, form-name == the filename in metadata.
//
// Constraints auditloop enforces: <=200 pages, viewport in {mobile,desktop},
// screenshots sniff as PNG/JPEG, 16 MiB/file, 64 MiB total. The page-count/viewport/
// finding-type/ref constraints are self-checked by Validate; the 16 MiB/file +
// 64 MiB total size caps are self-checked by ValidateFiles (both before POSTing).

// PushEndpoint is the ingestion path (mirrors plugin.PushEndpoint).
const PushEndpoint = "/api/plugins/runs"

// Viewport identity values (mirror plugin.ViewportMobile/Desktop).
const (
	ViewportMobile  = "mobile"
	ViewportDesktop = "desktop"
)

// EnvLab marks a hermetic localhost/DEV_MODE capture — perf numbers are not
// field-representative, so auditloop keeps them but suppresses the perf FINDINGS.
const EnvLab = "lab"

// PushPayload is the top-level `metadata` body.
type PushPayload struct {
	Label       string     `json:"label,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Pages       []PushPage `json:"pages"`
}

// PushPage is one captured view. url is the STABLE page identity auditloop's P2
// diff matches on across pushes.
type PushPage struct {
	URL        string `json:"url"`
	Viewport   string `json:"viewport"`
	Screenshot string `json:"screenshot"`
	Axe        string `json:"axe,omitempty"`
	Network    string `json:"network,omitempty"`
	// A11yDigest is the OPTIONAL filename of this view's bounded DOM/accessibility
	// digest part (a11y-digest.js output; see a11y.go). auditloop validates it
	// STRICTLY at ingest and a violation 400s the WHOLE push — including a digest that
	// decodes to all-empty lists — so it is set only when nonEmptyA11yDigest passes.
	// Omitting it is always safe: auditloop then evaluates that page screenshot-only,
	// exactly as before this field existed.
	A11yDigest        string        `json:"a11y_digest,omitempty"`
	AxeViolations     int           `json:"axe_violations"`
	ConsoleFirstParty int           `json:"console_first_party"`
	ConsoleThirdParty int           `json:"console_third_party"`
	NetworkFirstParty int           `json:"network_first_party"`
	NetworkThirdParty int           `json:"network_third_party"`
	Findings          []PushFinding `json:"findings,omitempty"`
}

// PushFinding is one normalized issue on a page.
type PushFinding struct {
	Type     string `json:"type"`     // a11y|console|network|perf|layout|other
	Severity string `json:"severity"` // free text
	Detail   string `json:"detail"`   // rendered escaped server-side
}

// PushResult is the JSON auditloop returns on a successful push.
type PushResult struct {
	RunID string `json:"run_id"`
	URL   string `json:"url"`
}

// ReferencedFiles returns the multipart filenames the payload references.
func (p *PushPayload) ReferencedFiles() map[string]bool {
	out := map[string]bool{}
	for _, pg := range p.Pages {
		for _, f := range []string{pg.Screenshot, pg.Axe, pg.Network, pg.A11yDigest} {
			if f != "" {
				out[f] = true
			}
		}
	}
	return out
}

// MaxPages caps the pages a single push may carry (mirrors plugin.MaxPages, the
// server's abuse guard). The harness self-checks against it before pushing.
const MaxPages = 200

// Size caps mirror auditloop's server-side push limits (handlers/plugins.go:
// http.MaxBytesReader 64 MiB total, per-file 16 MiB). ValidateFiles enforces them
// client-side so an oversized artifact is rejected here (and in tests) rather than
// surfacing as a remote 413.
const (
	MaxFileBytes  = 16 << 20 // 16 MiB per uploaded file part
	MaxTotalBytes = 64 << 20 // 64 MiB total multipart body budget
)

// ValidateFiles enforces the per-file (16 MiB) and total (64 MiB) size caps that
// auditloop applies server-side, so an oversized artifact fails locally before the
// POST. It is separate from Validate (which vets the metadata shape and has only
// filenames, not sizes) because only the caller holds the actual bytes. In Walk
// this runs before artifacts are persisted, so an oversize fails the whole walk
// (fail-loud beats a server-side 413); on the MaybePush path an oversize just
// skips the push, non-fatally.
//
// metaJSON is the `metadata` part and COUNTS TOWARD THE TOTAL, because the server's
// 64 MiB http.MaxBytesReader wraps the WHOLE multipart body, not just the file parts.
// It used to be excluded, which was harmless while metadata was a few KB of roll-up
// counts — but now that each page carries FINDINGS (axe violation details, console/
// network text) the metadata part is the one part that scales with page content: a
// max-pathological payload measures ~11 MiB. Omitting it under-reported the real body
// by that much, so the self-check could pass a body the server would 413.
func (p *PushPayload) ValidateFiles(metaJSON []byte, files map[string][]byte) error {
	total := int64(len(metaJSON))
	// NOTE the per-part cap is a SELF-IMPOSED sanity bound for the metadata part, not a
	// server limit: auditloop applies MaxFileBytes to uploaded FILE parts, while the
	// `metadata` VALUE part is bounded instead by ParseMultipartForm's 16 MiB plus Go's
	// ~10 MiB non-file reserve (≈26 MiB), under the 64 MiB body total. Being stricter
	// than the server is safe (a realistic walk's metadata is orders of magnitude
	// smaller), so the message must not misattribute the limit to auditloop.
	if total > MaxFileBytes {
		return fmt.Errorf("metadata part is %d bytes, over this harness's self-imposed %d-byte (16 MiB) metadata bound (auditloop itself allows ~26 MiB of non-file parts)", total, MaxFileBytes)
	}
	for name, data := range files {
		n := int64(len(data))
		if n > MaxFileBytes {
			return fmt.Errorf("file %q is %d bytes, over the %d-byte (16 MiB) per-file cap", name, n, MaxFileBytes)
		}
		total += n
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("total upload (metadata + %d files) is %d bytes, over the %d-byte (64 MiB) cap", len(files), total, MaxTotalBytes)
	}
	return nil
}

// Validate checks the payload against the set of multipart filenames that will
// accompany it, mirroring auditloop's server-side plugin.Validate so a malformed
// push is caught here (and in tests) rather than surfacing as a remote 400. It
// enforces: 1..MaxPages pages; a non-empty stable url + a known viewport + a
// screenshot ref per page; a known finding type; and strict multipart integrity —
// every referenced filename has a matching part AND every provided part is
// referenced (no orphan uploads).
func (p *PushPayload) Validate(provided map[string]bool) error {
	if p.Environment != "" && p.Environment != EnvLab && p.Environment != "staging" && p.Environment != "prod" {
		return fmt.Errorf("environment must be lab, staging or prod, got %q", p.Environment)
	}
	if len(p.Pages) == 0 {
		return fmt.Errorf("no pages in payload")
	}
	if len(p.Pages) > MaxPages {
		return fmt.Errorf("too many pages: %d (max %d)", len(p.Pages), MaxPages)
	}
	// The closed finding-type set auditloop accepts. a11y/console/network are the ones
	// this harness emits (see findings.go); perf/layout are computed server-side from
	// the raw blocks and "other" is the catch-all — accepted here for wire parity.
	validType := map[string]bool{
		FindingA11y: true, FindingConsole: true, FindingNetwork: true,
		"perf": true, "layout": true, "other": true,
	}
	seenRefs := map[string]bool{}
	for i, pg := range p.Pages {
		if strings.TrimSpace(pg.URL) == "" {
			return fmt.Errorf("page %d: url (stable page identity) is required", i)
		}
		if pg.Viewport != ViewportMobile && pg.Viewport != ViewportDesktop {
			return fmt.Errorf("page %d: viewport must be %q or %q, got %q", i, ViewportMobile, ViewportDesktop, pg.Viewport)
		}
		if strings.TrimSpace(pg.Screenshot) == "" {
			return fmt.Errorf("page %d: screenshot filename is required", i)
		}
		for _, ref := range []string{pg.Screenshot, pg.Axe, pg.Network, pg.A11yDigest} {
			if ref == "" {
				continue
			}
			if !provided[ref] {
				return fmt.Errorf("page %d references file %q but no matching part was provided", i, ref)
			}
			seenRefs[ref] = true
		}
		for j, f := range pg.Findings {
			if !validType[f.Type] {
				return fmt.Errorf("page %d finding %d: unknown type %q", i, j, f.Type)
			}
		}
	}
	for name := range provided {
		if !seenRefs[name] {
			return fmt.Errorf("provided file %q is not referenced by any page", name)
		}
	}
	return nil
}

// Upload builds the multipart body (a `metadata` field = metaJSON + one file part
// per entry in files, each part's form-name == its filename) and POSTs it with the
// bearer token. Mirrors plugin.Upload. A non-2xx yields an error with the server's
// (credential-free) message.
func Upload(ctx context.Context, client *http.Client, baseURL, token string, metaJSON []byte, files map[string][]byte) (*PushResult, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("metadata", string(metaJSON)); err != nil {
		return nil, err
	}
	for name, data := range files {
		fw, err := mw.CreateFormFile(name, name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	url := strings.TrimRight(baseURL, "/") + PushEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("push failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var res PushResult
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, fmt.Errorf("push succeeded but response was not understood: %w", err)
	}
	return &res, nil
}
