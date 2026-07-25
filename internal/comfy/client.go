package comfy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Response-size ceilings. The ComfyUI server's responses are UNTRUSTED input
// (even a local one can be compromised or buggy), so every read is bounded to
// keep a malformed/hostile response from exhausting memory.
const (
	// maxJSONBytes bounds the small JSON bodies (submit/error/history/queue/stats).
	maxJSONBytes = 8 << 20
	// maxObjectInfoBytes bounds /object_info, which enumerates every installed node
	// (and every model filename in each loader's choices) and is legitimately large
	// on a full install — several MB is normal.
	maxObjectInfoBytes = 64 << 20
	// maxImageBytes bounds a single result image fetched via /view.
	maxImageBytes = 64 << 20
)

// primitiveWidgetTypes are the ComfyUI input type names that denote a WIDGET (a
// value the user enters in the node) rather than a link/connection input. A bare
// list-of-choices (a combo) is also a widget; that case is handled in IsWidget.
var primitiveWidgetTypes = map[string]bool{
	"INT": true, "FLOAT": true, "STRING": true, "BOOLEAN": true, "COMBO": true,
}

// Client is a poll-based HTTP client for a LOCAL ComfyUI server.
//
// It is deliberately a SEPARATE *http.Client from the civitai SDK's client: that
// one carries an SSRF guard that blocks loopback/private targets, which is exactly
// where ComfyUI runs (127.0.0.1:8188). This client talks only to the operator's
// config-supplied comfy_url (never a per-request target), so loopback is the
// intended, safe destination.
//
// ComfyUI has no authentication by default. An OPTIONAL bearer token is sent when
// configured (for installs fronted by ComfyUI-Login); a 401 is surfaced as a clear
// error so the operator knows a token is required.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// NewClient builds a ComfyUI client for baseURL with an optional bearer token.
// The trailing slash on baseURL is trimmed so path joins stay clean.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http: &http.Client{
			Timeout: 120 * time.Second,
			// This client intentionally has NO SSRF guard (it must reach loopback,
			// where ComfyUI runs). ComfyUI's real API never redirects these
			// endpoints, so do NOT follow redirects — otherwise a hostile/compromised
			// comfy_url could 3xx a request (e.g. /view, whose bytes we proxy back to
			// the browser) at an internal metadata service. (audit 🟡)
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		token: strings.TrimSpace(token),
	}
}

// BaseURL returns the (trailing-slash-trimmed) server URL, for diagnostics.
func (c *Client) BaseURL() string { return c.baseURL }

// do issues an HTTP request to the ComfyUI server, attaching the JSON content-type
// (when a body is present) and the optional bearer token.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// readBounded reads at most max bytes from r (a defensive bound on an untrusted
// server response).
func readBounded(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// statusError builds an error for a non-success HTTP status, including a bounded
// body snippet. A 401 is called out explicitly (an optional login token is needed).
func statusError(op string, status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 300 {
		snippet = snippet[:300] + "…"
	}
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%s: 401 unauthorized — this ComfyUI requires a login token (set comfy_token): %s", op, snippet)
	}
	return fmt.Errorf("%s: unexpected status %d: %s", op, status, snippet)
}

// --- /prompt (submit) ---

// SubmitResult is the parsed HTTP-200 response of POST /prompt.
type SubmitResult struct {
	PromptID   string          `json:"prompt_id"`
	Number     int             `json:"number"`
	NodeErrors json.RawMessage `json:"node_errors"`
}

// PromptValidationError is the typed HTTP-400 response of POST /prompt: ComfyUI
// rejected the graph before queuing it. It surfaces the top-level message and the
// per-node errors so the UI can tell the user exactly what is wrong.
type PromptValidationError struct {
	Status     int
	Type       string
	Message    string
	Details    string
	NodeErrors map[string]json.RawMessage
}

func (e *PromptValidationError) Error() string {
	if e == nil {
		return "comfy: prompt validation error"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "prompt rejected by ComfyUI"
	}
	if n := len(e.NodeErrors); n > 0 {
		unit := "node error"
		if n != 1 {
			unit += "s"
		}
		msg = fmt.Sprintf("%s (%d %s)", msg, n, unit)
	}
	return "comfy: " + msg
}

// parseValidationError parses a 400 /prompt body into a PromptValidationError.
// Parsing is best-effort: a malformed body still yields a non-nil error.
func parseValidationError(status int, body []byte) *PromptValidationError {
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Details string `json:"details"`
		} `json:"error"`
		NodeErrors map[string]json.RawMessage `json:"node_errors"`
	}
	_ = json.Unmarshal(body, &parsed)
	return &PromptValidationError{
		Status:     status,
		Type:       parsed.Error.Type,
		Message:    strings.TrimSpace(parsed.Error.Message),
		Details:    strings.TrimSpace(parsed.Error.Details),
		NodeErrors: parsed.NodeErrors,
	}
}

// Submit POSTs an api-format graph to /prompt. clientID/promptID are optional (a
// caller-chosen promptID makes the run addressable in /history and /queue). On
// HTTP 200 it returns the SubmitResult; on HTTP 400 a *PromptValidationError; any
// other status an error carrying the status + a body snippet.
func (c *Client) Submit(ctx context.Context, apiGraph json.RawMessage, clientID, promptID string) (*SubmitResult, error) {
	payload := struct {
		Prompt   json.RawMessage `json:"prompt"`
		ClientID string          `json:"client_id,omitempty"`
		PromptID string          `json:"prompt_id,omitempty"`
	}{Prompt: apiGraph, ClientID: clientID, PromptID: promptID}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal prompt: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/prompt", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("submit prompt: %w", err)
	}
	defer resp.Body.Close()
	data, _ := readBounded(resp.Body, maxJSONBytes)

	switch resp.StatusCode {
	case http.StatusOK:
		var sr SubmitResult
		if err := json.Unmarshal(data, &sr); err != nil {
			return nil, fmt.Errorf("parse submit response: %w", err)
		}
		return &sr, nil
	case http.StatusBadRequest:
		return nil, parseValidationError(resp.StatusCode, data)
	default:
		return nil, statusError("submit prompt", resp.StatusCode, data)
	}
}

// --- /object_info (schema) ---

// InputSpec is one input's schema entry: a JSON array [type, config?]. type is
// either a STRING (a primitive widget type like "INT" or a node-output type like
// "MODEL") or a LIST of choices (a combo widget). Config carries default/min/max/
// control_after_generate where present.
type InputSpec struct {
	// TypeName is the first element when it is a string ("INT", "MODEL", "COMBO",
	// …); empty when the first element is a list of choices.
	TypeName string
	// IsCombo is true when the first element is a list of choices (a combo widget).
	IsCombo bool
	// Choices is the combo's options when they are strings; nil otherwise.
	Choices []string
	// Config is the optional second element (an object of default/min/max/…).
	Config map[string]json.RawMessage
}

// UnmarshalJSON parses a [type, config?] spec array. It is permissive: an
// unexpected shape yields a zero-ish spec rather than an error, so one odd input
// can never fail the whole /object_info parse.
func (s *InputSpec) UnmarshalJSON(b []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil || len(arr) == 0 {
		return nil
	}
	if isJSONArray(arr[0]) {
		s.IsCombo = true
		_ = json.Unmarshal(arr[0], &s.Choices) // best-effort (non-string choices stay nil)
	} else {
		_ = json.Unmarshal(arr[0], &s.TypeName)
	}
	if len(arr) >= 2 {
		_ = json.Unmarshal(arr[1], &s.Config)
	}
	return nil
}

// IsWidget reports whether the input is a WIDGET (a value entered in the node)
// rather than a link input. An input is a widget when its type is a primitive
// widget type (INT/FLOAT/STRING/BOOLEAN/COMBO) OR a list of choices (a combo);
// any other type (an uppercase node-output type like MODEL/LATENT/CLIP) is a link.
func (s InputSpec) IsWidget() bool {
	if s.IsCombo {
		return true
	}
	return primitiveWidgetTypes[strings.ToUpper(strings.TrimSpace(s.TypeName))]
}

// IsWidget is the package-level form of InputSpec.IsWidget, per ComfyUI's rule.
func IsWidget(s InputSpec) bool { return s.IsWidget() }

// ControlAfterGenerate reports whether this widget carries a control_after_generate
// companion (seed/noise_seed widgets), which in a UI graph's widgets_values
// consumes an EXTRA slot (the fixed/increment/randomize control) that has no schema
// input. The converter must skip that extra value.
func (s InputSpec) ControlAfterGenerate() bool {
	raw, ok := s.Config["control_after_generate"]
	if !ok {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	// Some versions encode it as a default control string ("randomize"); its mere
	// presence still means the companion widget exists.
	return true
}

// ConfigValue returns a raw config entry (e.g. "default", "min", "max") if present.
func (s InputSpec) ConfigValue(key string) (json.RawMessage, bool) {
	v, ok := s.Config[key]
	return v, ok
}

// NodeSchema is one node type's /object_info entry: its required/optional inputs
// and the authoritative widget ORDER (input_order), which the converter walks to
// map a UI graph's flat widgets_values onto named inputs.
type NodeSchema struct {
	Input struct {
		Required map[string]InputSpec `json:"required"`
		Optional map[string]InputSpec `json:"optional"`
	} `json:"input"`
	InputOrder struct {
		Required []string `json:"required"`
		Optional []string `json:"optional"`
	} `json:"input_order"`
}

// ObjectInfo maps a node type name to its schema.
type ObjectInfo map[string]NodeSchema

// ObjectInfo fetches GET /object_info (the installed-node schema) used by the
// converter and preflight.
func (c *Client) ObjectInfo(ctx context.Context) (ObjectInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/object_info", nil)
	if err != nil {
		return nil, fmt.Errorf("object_info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return nil, statusError("object_info", resp.StatusCode, data)
	}
	data, err := readBounded(resp.Body, maxObjectInfoBytes)
	if err != nil {
		return nil, fmt.Errorf("read object_info: %w", err)
	}
	var oi ObjectInfo
	if err := json.Unmarshal(data, &oi); err != nil {
		return nil, fmt.Errorf("parse object_info: %w", err)
	}
	return oi, nil
}

// --- /history ---

// ImageRef locates one output image on the ComfyUI server (fetchable via /view).
type ImageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// NodeOutput is one node's outputs in a history entry (we care about images).
type NodeOutput struct {
	Images []ImageRef `json:"images"`
}

// HistoryStatus is a history entry's status block.
type HistoryStatus struct {
	Completed bool            `json:"completed"`
	StatusStr string          `json:"status_str"`
	Messages  json.RawMessage `json:"messages"`
}

// HistoryEntry is one prompt's /history record: its per-node outputs and status.
type HistoryEntry struct {
	Outputs map[string]NodeOutput `json:"outputs"`
	Status  HistoryStatus         `json:"status"`
}

// History fetches GET /history/{promptID} and returns that prompt's entry, or
// (nil, nil) when the id is not present yet (still queued/running).
func (c *Client) History(ctx context.Context, promptID string) (*HistoryEntry, error) {
	resp, err := c.do(ctx, http.MethodGet, "/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return nil, statusError("history", resp.StatusCode, data)
	}
	data, err := readBounded(resp.Body, maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	var hist map[string]HistoryEntry
	if err := json.Unmarshal(data, &hist); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}
	entry, ok := hist[promptID]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

// AllImages flattens every output node's images, in node-key-sorted order for a
// stable gallery.
func (e *HistoryEntry) AllImages() []ImageRef {
	if e == nil {
		return nil
	}
	var out []ImageRef
	for _, node := range e.Outputs {
		out = append(out, node.Images...)
	}
	return out
}

// --- /queue ---

// QueueState reports whether promptID is currently running or pending in the
// ComfyUI queue. queuedPos is the 0-based index within the pending list (how many
// are ahead) when pending; found is false when the id is in neither list.
func (c *Client) QueueState(ctx context.Context, promptID string) (running bool, queuedPos int, found bool, err error) {
	resp, e := c.do(ctx, http.MethodGet, "/queue", nil)
	if e != nil {
		return false, 0, false, fmt.Errorf("queue: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return false, 0, false, statusError("queue", resp.StatusCode, data)
	}
	data, e := readBounded(resp.Body, maxJSONBytes)
	if e != nil {
		return false, 0, false, fmt.Errorf("read queue: %w", e)
	}
	var q struct {
		Running []json.RawMessage `json:"queue_running"`
		Pending []json.RawMessage `json:"queue_pending"`
	}
	if e := json.Unmarshal(data, &q); e != nil {
		return false, 0, false, fmt.Errorf("parse queue: %w", e)
	}
	for _, entry := range q.Running {
		if queueEntryMatches(entry, promptID) {
			return true, 0, true, nil
		}
	}
	for i, entry := range q.Pending {
		if queueEntryMatches(entry, promptID) {
			return false, i, true, nil
		}
	}
	return false, 0, false, nil
}

// queueEntryMatches reports whether a queue tuple [number, prompt_id, …] is for
// promptID (its second element).
func queueEntryMatches(entry json.RawMessage, promptID string) bool {
	var tuple []json.RawMessage
	if json.Unmarshal(entry, &tuple) != nil || len(tuple) < 2 {
		return false
	}
	var id string
	if json.Unmarshal(tuple[1], &id) != nil {
		return false
	}
	return id == promptID
}

// --- /view ---

// View fetches one output image via GET /view (filename+subfolder+type all sent;
// subfolder may be ""), returning the bytes and content-type. The read is bounded.
func (c *Client) View(ctx context.Context, ref ImageRef) ([]byte, string, error) {
	q := url.Values{}
	q.Set("filename", ref.Filename)
	q.Set("subfolder", ref.Subfolder)
	q.Set("type", ref.Type)
	resp, err := c.do(ctx, http.MethodGet, "/view?"+q.Encode(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("view: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return nil, "", statusError("view", resp.StatusCode, data)
	}
	ct := resp.Header.Get("Content-Type")
	data, err := readBounded(resp.Body, maxImageBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return data, ct, nil
}

// --- /interrupt ---

// Interrupt POSTs /interrupt to cancel the currently-executing prompt.
func (c *Client) Interrupt(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, "/interrupt", nil)
	if err != nil {
		return fmt.Errorf("interrupt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return statusError("interrupt", resp.StatusCode, data)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// --- /system_stats ---

// SystemStats is a health/version probe of the ComfyUI server.
type SystemStats struct {
	// ComfyUIVersion is lifted from System.ComfyUIVersion after parsing.
	ComfyUIVersion string `json:"-"`
	System         struct {
		ComfyUIVersion string `json:"comfyui_version"`
		OS             string `json:"os"`
		PythonVersion  string `json:"python_version"`
	} `json:"system"`
}

// SystemStats fetches GET /system_stats (a reachability + version check).
func (c *Client) SystemStats(ctx context.Context) (*SystemStats, error) {
	resp, err := c.do(ctx, http.MethodGet, "/system_stats", nil)
	if err != nil {
		return nil, fmt.Errorf("system_stats: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return nil, statusError("system_stats", resp.StatusCode, data)
	}
	data, err := readBounded(resp.Body, maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read system_stats: %w", err)
	}
	var st SystemStats
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse system_stats: %w", err)
	}
	st.ComfyUIVersion = st.System.ComfyUIVersion
	return &st, nil
}

// NewID returns a random UUID v4 string suitable for a ComfyUI client_id /
// prompt_id. ComfyUI (>= 0.x frontend) REQUIRES prompt_id to be a valid UUID and
// rejects a bare hex string with HTTP 400 "prompt_id must be a valid UUID", so
// this MUST stay UUID-formatted — verified against a live ComfyUI 0.27.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Deterministic fallback still shaped as a UUID (v4/variant bits below).
		copy(b, []byte(time.Now().String()))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
