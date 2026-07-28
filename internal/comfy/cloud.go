package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultCloudBaseURL is the CivitAI orchestration API base URL. Cloud workflow
// runs are submitted here (authenticated with the user's existing CivitAI token).
const DefaultCloudBaseURL = "https://orchestration.civitai.com"

// maxCloudJSONBytes bounds a cloud API response body. Workflow objects (cost,
// transactions, steps, blobs) are small; this is a defensive ceiling on an
// untrusted remote response.
const maxCloudJSONBytes = 16 << 20

// --- request types ---

// CloudTemplate is the WorkflowTemplate POSTed to the orchestration API. It is a
// list of steps; C1 always sends exactly one customComfy step.
type CloudTemplate struct {
	Steps []CloudStep `json:"steps"`
}

// CloudStep is one step of a template. $type selects the step kind; C1 uses
// "customComfy" exclusively.
type CloudStep struct {
	Type  string          `json:"$type"`
	Name  string          `json:"name"`
	Input CloudComfyInput `json:"input"`
}

// CloudComfyInput is the customComfy step's input: the resource AIR URNs, the raw
// API-format graph (opaque to the orchestrator), and the trace level.
type CloudComfyInput struct {
	// Resources are the AIR URN strings for EVERY resource the graph references
	// (checkpoints/LoRAs/VAE as civitai model URNs, custom nodes as nodepack URNs).
	Resources []string `json:"resources"`
	// Workflow is the raw API-format graph object (flat {nodeId:{class_type,...}}).
	Workflow json.RawMessage `json:"workflow"`
	// Trace is one of "none"|"logs"|"binary"; C1 always sends "none".
	Trace string `json:"trace"`
	// MinimumDurationSeconds is the orchestrator's submit-time affordability gate:
	// when set, the workflow is rejected at submit (→ HTTP 400) unless the user can
	// afford at least this many seconds of generation. A nil pointer OMITS the field
	// entirely (no gate — the "run anyway" case); a set pointer serializes the int.
	// The live-balance guard still cancels mid-run if the balance later goes negative.
	MinimumDurationSeconds *int `json:"minimumDurationSeconds,omitempty"`
}

// NewCustomComfyTemplate builds a single-step customComfy template from an
// API-format graph and its resolved resource URNs.
func NewCustomComfyTemplate(apiGraph json.RawMessage, resources []string) CloudTemplate {
	if resources == nil {
		resources = []string{}
	}
	return CloudTemplate{
		Steps: []CloudStep{{
			Type: "customComfy",
			Name: "comfy",
			Input: CloudComfyInput{
				Resources: resources,
				Workflow:  apiGraph,
				Trace:     "none",
			},
		}},
	}
}

// WithMinimumDuration returns a COPY of the template whose customComfy step(s)
// carry the submit-time affordability gate set to sec seconds. The receiver is
// left unmodified (its Input.MinimumDurationSeconds stays nil → omitted), so a
// caller can build both a gated and an un-gated template from one base. Callers
// that want no gate simply skip this (the field stays nil and is omitted).
func (t CloudTemplate) WithMinimumDuration(sec int) CloudTemplate {
	steps := make([]CloudStep, len(t.Steps))
	copy(steps, t.Steps)
	for i := range steps {
		v := sec
		steps[i].Input.MinimumDurationSeconds = &v
	}
	return CloudTemplate{Steps: steps}
}

// --- response types ---

// CloudWorkflow is the orchestration API's Workflow object (the subset the UI
// needs). Untrusted remote data — every string rendered from it is escaped.
type CloudWorkflow struct {
	ID          string              `json:"id"`
	Status      string              `json:"status"`
	CreatedAt   string              `json:"createdAt"`
	StartedAt   string              `json:"startedAt"`
	CompletedAt string              `json:"completedAt"`
	Cost        *CloudCost          `json:"cost"`
	Transaction *CloudTransactions  `json:"transactions"`
	Steps       []CloudWorkflowStep `json:"steps"`
}

// CloudCost is the WorkflowCost: base Buzz plus optional named factors.
type CloudCost struct {
	Base    float64            `json:"base"`
	Factors map[string]float64 `json:"factors"`
}

// CloudTransactions carries the whatif "can't afford" flag.
type CloudTransactions struct {
	InsufficientBuzz *bool `json:"insufficientBuzz"`
}

// CloudWorkflowStep is one step of a Workflow response; a customComfy step's
// output carries the result blobs.
type CloudWorkflowStep struct {
	Name   string           `json:"name"`
	Status string           `json:"status"`
	Output *CloudStepOutput `json:"output"`
}

// CloudStepOutput holds a step's result blobs.
type CloudStepOutput struct {
	Blobs []CloudBlob `json:"blobs"`
}

// CloudBlob is one output artifact; URL is a civitai CDN image URL when available.
type CloudBlob struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Available bool   `json:"available"`
	Type      string `json:"type"`
}

// Cloud workflow status enum values.
const (
	CloudStatusUnassigned = "unassigned"
	CloudStatusPreparing  = "preparing"
	CloudStatusScheduled  = "scheduled"
	CloudStatusProcessing = "processing"
	CloudStatusSucceeded  = "succeeded"
	CloudStatusFailed     = "failed"
	CloudStatusExpired    = "expired"
	CloudStatusCanceled   = "canceled"
)

// IsTerminal reports whether a status is a settled (no-longer-changing) state.
func CloudStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case CloudStatusSucceeded, CloudStatusFailed, CloudStatusExpired, CloudStatusCanceled:
		return true
	}
	return false
}

// BaseCost returns the workflow's base Buzz cost (0 when absent).
func (wf *CloudWorkflow) BaseCost() float64 {
	if wf == nil || wf.Cost == nil {
		return 0
	}
	return wf.Cost.Base
}

// InsufficientBuzz reports whether the whatif estimate flagged the account as
// unable to afford the run.
func (wf *CloudWorkflow) InsufficientBuzz() bool {
	if wf == nil || wf.Transaction == nil || wf.Transaction.InsufficientBuzz == nil {
		return false
	}
	return *wf.Transaction.InsufficientBuzz
}

// BlobURLs collects every step output blob URL (civitai CDN image URLs), in step
// then blob order, skipping blank URLs.
func (wf *CloudWorkflow) BlobURLs() []string {
	if wf == nil {
		return nil
	}
	var out []string
	for _, st := range wf.Steps {
		if st.Output == nil {
			continue
		}
		for _, b := range st.Output.Blobs {
			if strings.TrimSpace(b.URL) != "" {
				out = append(out, b.URL)
			}
		}
	}
	return out
}

// --- error type ---

// CloudProblem is the RFC7807 ProblemDetails body the orchestration API returns
// on a non-2xx status (400/401/403/429/…). Title/Detail are surfaced to the user
// (escaped at render time — they are remote/untrusted).
type CloudProblem struct {
	StatusCode int    // the HTTP status actually returned
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Status     int    `json:"status"`
}

// IsBadRequest reports whether the problem is an HTTP 400. The orchestrator
// rejects a submit that fails the minimumDurationSeconds affordability gate with a
// 400; callers use this to offer a "run anyway" (gate-skipped) retry.
func (p *CloudProblem) IsBadRequest() bool {
	return p != nil && p.StatusCode == http.StatusBadRequest
}

func (e *CloudProblem) Error() string {
	if e == nil {
		return "cloud: error"
	}
	msg := strings.TrimSpace(e.Detail)
	if msg == "" {
		msg = strings.TrimSpace(e.Title)
	}
	if msg == "" {
		msg = fmt.Sprintf("orchestration API returned status %d", e.StatusCode)
	}
	return "cloud: " + msg
}

// parseCloudProblem parses a non-2xx body into a *CloudProblem. Parsing is
// best-effort: a body that is a bare string (some 403s) or unparseable still
// yields a non-nil, informative error.
func parseCloudProblem(status int, body []byte) *CloudProblem {
	p := &CloudProblem{StatusCode: status, Status: status}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return p
	}
	if trimmed[0] == '{' {
		_ = json.Unmarshal(trimmed, p)
		p.StatusCode = status
		if p.Status == 0 {
			p.Status = status
		}
		return p
	}
	// A bare string body (e.g. a 403 "Forbidden"): use it as the detail.
	var s string
	if json.Unmarshal(trimmed, &s) == nil && s != "" {
		p.Detail = s
		return p
	}
	snippet := string(trimmed)
	if len(snippet) > 300 {
		snippet = snippet[:300] + "…"
	}
	p.Detail = snippet
	return p
}

// --- client ---

// CloudClient is a poll-based HTTP client for the CivitAI orchestration API.
//
// It is a SEPARATE *http.Client from the civitai SDK download client: that one
// carries an SSRF guard that would interfere. This client talks only to the
// config-supplied orchestration base URL (never a per-request target), and mirrors
// client.go's posture — a bounded timeout and a redirect guard (the real API does
// not redirect these endpoints; following a 3xx to an internal target with the
// bearer token attached would be a leak).
type CloudClient struct {
	baseURL string
	http    *http.Client
	token   string
}

// NewCloudClient builds a cloud orchestration client for baseURL (empty → the
// default) with the caller's CivitAI bearer token.
func NewCloudClient(baseURL, token string) *CloudClient {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultCloudBaseURL
	}
	return &CloudClient{
		baseURL: base,
		http: &http.Client{
			Timeout: 120 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		token: strings.TrimSpace(token),
	}
}

// BaseURL returns the (trailing-slash-trimmed) orchestration base URL.
func (c *CloudClient) BaseURL() string { return c.baseURL }

func (c *CloudClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	}
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// SubmitCloud POSTs a template to /v2/consumer/workflows. When whatif is true the
// orchestrator returns a populated Workflow (cost + transactions) WITHOUT running
// or spending. Both 200 and 202 carry a Workflow with an id; any other status is
// decoded into a *CloudProblem.
func (c *CloudClient) SubmitCloud(ctx context.Context, template CloudTemplate, whatif bool) (*CloudWorkflow, error) {
	b, err := json.Marshal(template)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	q := url.Values{}
	q.Set("whatif", strconv.FormatBool(whatif))
	resp, err := c.do(ctx, http.MethodPost, "/v2/consumer/workflows?"+q.Encode(), b)
	if err != nil {
		return nil, fmt.Errorf("submit cloud workflow: %w", err)
	}
	defer resp.Body.Close()
	// readErr (an oversized body) is fatal only on the SUCCESS path, where the bytes
	// are parsed as the authoritative response; the problem path deliberately uses
	// the truncated text as a snippet.
	data, readErr := readBounded(resp.Body, maxCloudJSONBytes)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, parseCloudProblem(resp.StatusCode, data)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read cloud workflow: %w", readErr)
	}
	var wf CloudWorkflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse cloud workflow: %w", err)
	}
	return &wf, nil
}

// GetCloudWorkflow polls GET /v2/consumer/workflows/{id} for a workflow's current
// state. A non-2xx is decoded into a *CloudProblem.
func (c *CloudClient) GetCloudWorkflow(ctx context.Context, id string) (*CloudWorkflow, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v2/consumer/workflows/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, fmt.Errorf("get cloud workflow: %w", err)
	}
	defer resp.Body.Close()
	// As above: an oversized body is fatal only where it is parsed, not where it is
	// only a problem-response snippet.
	data, readErr := readBounded(resp.Body, maxCloudJSONBytes)
	if resp.StatusCode/100 != 2 {
		return nil, parseCloudProblem(resp.StatusCode, data)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read cloud workflow: %w", readErr)
	}
	var wf CloudWorkflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse cloud workflow: %w", err)
	}
	return &wf, nil
}

// CancelCloudWorkflow requests cancellation via PUT /v2/consumer/workflows/{id}
// with status "canceled" (the SDK's UpdateWorkflowStatus). Best-effort: a non-2xx
// is decoded into a *CloudProblem.
func (c *CloudClient) CancelCloudWorkflow(ctx context.Context, id string) error {
	body, err := json.Marshal(struct {
		Status string `json:"status"`
	}{Status: CloudStatusCanceled})
	if err != nil {
		return fmt.Errorf("marshal cancel: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPut, "/v2/consumer/workflows/"+url.PathEscape(id), body)
	if err != nil {
		return fmt.Errorf("cancel cloud workflow: %w", err)
	}
	defer resp.Body.Close()
	data, _ := readBounded(resp.Body, maxCloudJSONBytes)
	if resp.StatusCode/100 != 2 {
		return parseCloudProblem(resp.StatusCode, data)
	}
	return nil
}
