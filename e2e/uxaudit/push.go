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
// Constraints auditloop enforces (respected by BuildPayload): <=200 pages,
// viewport in {mobile,desktop}, screenshots sniff as PNG/JPEG, 16 MiB/file,
// 64 MiB total.

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
	URL               string        `json:"url"`
	Viewport          string        `json:"viewport"`
	Screenshot        string        `json:"screenshot"`
	Axe               string        `json:"axe,omitempty"`
	Network           string        `json:"network,omitempty"`
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
		for _, f := range []string{pg.Screenshot, pg.Axe, pg.Network} {
			if f != "" {
				out[f] = true
			}
		}
	}
	return out
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
