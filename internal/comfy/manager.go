package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ComfyUI-Manager client. Manager is an OPTIONAL third-party custom node, and
// this whole file is best-effort: absence is a normal state, every failure
// degrades to attribution-plus-manual-command, and no Manager response is
// load-bearing for correctness.
//
// Everything here is measured against the live Manager V3.41 on 127.0.0.1:8188
// (2026-07-29) plus its source in custom_nodes/ComfyUI-Manager/glob/. The
// non-obvious parts:
//
//   - Manager is not ONE API. V3, V4-default and V4-legacy-UI expose different
//     route sets, and neither V4 module registers both. Probe; never hard-code.
//   - "The directory exists" and "the package imported" are FALSE POSITIVES:
//     Manager's CLI-only mode loads with the web API disabled. Only a live
//     endpoint response counts as present.
//   - getlist/getmappings read `request.rel_url.query["mode"]` with BRACKET
//     access, so omitting mode= is a Python KeyError -> HTTP 500. Verified live:
//     both endpoints 500 without it.
//   - Reboot is os.execv with no queue inspection. It will destroy a running
//     generation. We refuse it ourselves.

// Manager response-size ceilings. getlist is ~4.5 MB live and getmappings
// ~1.4 MB; the caps are generous but finite because this is a third-party index
// fetched over the network into memory.
const (
	// maxManagerIndexBytes bounds getlist / getmappings.
	maxManagerIndexBytes = 96 << 20
	// maxManagerSmallBytes bounds the small Manager responses (version, status,
	// installed sets).
	maxManagerSmallBytes = 8 << 20
)

// Manager API lines. Each names a distinct route set; ManagerProbe reports which
// one (if any) is actually answering.
const (
	// ManagerLineNone means no live ComfyUI-Manager web API was found. This is a
	// normal state (Manager absent, or loaded in CLI-only mode with the web API
	// disabled), NOT an error.
	ManagerLineNone = ""
	// ManagerLineV3 is Manager V3 (`/api/manager/queue/*`, `/api/customnode/*`).
	ManagerLineV3 = "v3"
	// ManagerLineV4 is Manager V4 in its default mode (`/api/v2/manager/queue/task`).
	ManagerLineV4 = "v4"
	// ManagerLineV4Legacy is Manager V4 started with --enable-manager-legacy-ui
	// (`/api/v2/manager/queue/batch`).
	ManagerLineV4Legacy = "v4-legacy"
)

// ManagerInfo is the result of ManagerProbe: which Manager line is live and what
// it can do. Present==false is a normal, expected outcome.
type ManagerInfo struct {
	// Present is true only when a Manager endpoint actually ANSWERED. A directory
	// on disk or an imported module is not enough.
	Present bool
	// Line is the detected API line (ManagerLineV3 / V4 / V4Legacy), or
	// ManagerLineNone when absent.
	Line string
	// Version is Manager's self-reported version string (e.g. "V3.41"), when the
	// line exposes one.
	Version string
	// CanInstall is true when the detected line exposes an install route we know
	// how to drive.
	CanInstall bool
	// HasNodePackList is true when `getlist` is available. It is GONE in V4's
	// default mode, where attribution must lean on getmappings + the Registry.
	HasNodePackList bool
	// HasMappings is true when `getmappings` answered.
	HasMappings bool
	// Note is a short human-readable explanation of a degraded/absent state.
	Note string
}

// ErrManagerAbsent reports that no live ComfyUI-Manager web API was found. It is
// an expected condition: the caller must degrade to attribution + a manual
// install command, not surface a failure.
var ErrManagerAbsent = errors.New("comfy: no live ComfyUI-Manager API")

// ErrQueueBusy reports that a reboot was REFUSED because ComfyUI has queued or
// running work. Manager's own reboot does os.execv with no queue inspection and
// would destroy that generation, so this refusal is ours, not Manager's.
var ErrQueueBusy = errors.New("comfy: refusing to restart ComfyUI while its queue is busy")

// ManagerProbe detects whether a ComfyUI-Manager web API is live and which line
// it is, degrading cleanly to an absent (not errored) result.
//
// Probing uses GET-able, side-effect-free routes ONLY. That constraint is
// load-bearing and non-obvious: aiohttp answers a GET on a POST-only route with
// 404, indistinguishable from a route that does not exist (verified live —
// `GET /api/manager/queue/install` 404s on a Manager that has the route), so the
// install endpoints themselves cannot be probed without actually installing
// something. Each line is therefore identified by a read-only sibling of its
// install route:
//
//	V4 default    -> GET /api/v2/manager/queue/status
//	V4 legacy-UI  -> GET /api/v2/manager/queue/history
//	V3            -> GET /api/manager/queue/status
//
// The returned error is non-nil only for a genuine transport failure; an absent
// Manager yields (info with Present=false, nil).
func (c *Client) ManagerProbe(ctx context.Context) (*ManagerInfo, error) {
	info := &ManagerInfo{Line: ManagerLineNone}

	// Ordered so the newest line wins when a server somehow answers on several.
	lines := []struct {
		line  string
		probe string
	}{
		{ManagerLineV4, "/api/v2/manager/queue/status"},
		{ManagerLineV4Legacy, "/api/v2/manager/queue/history"},
		{ManagerLineV3, "/api/manager/queue/status"},
	}
	for _, l := range lines {
		ok, err := c.managerGETOK(ctx, l.probe)
		if err != nil {
			// A transport failure means ComfyUI itself is unreachable; that is a
			// real error, not "Manager is absent".
			return info, fmt.Errorf("probe ComfyUI-Manager: %w", err)
		}
		if ok {
			info.Present = true
			info.Line = l.line
			info.CanInstall = true
			break
		}
	}
	if !info.Present {
		info.Note = "ComfyUI-Manager's web API did not answer. It may not be installed, or it may be loaded in CLI-only mode (which disables the web API). Node packs can still be attributed and installed by hand."
		return info, nil
	}

	// Version is informational; its absence never downgrades the detection.
	if v, err := c.managerVersion(ctx, info.Line); err == nil {
		info.Version = v
	}

	// Index availability. getlist is GONE in V4's default mode, so probe rather
	// than infer it from the line.
	if _, err := c.ManagerMappings(ctx); err == nil {
		info.HasMappings = true
	}
	if _, err := c.ManagerNodePacks(ctx); err == nil {
		info.HasNodePackList = true
	}
	if !info.HasNodePackList {
		info.Note = "ComfyUI-Manager did not serve its node-pack list (it is absent in Manager V4's default mode), so pack details come from the class mappings and the Comfy Registry only."
	}
	return info, nil
}

// managerGETOK issues a GET and reports whether it answered 2xx. A 404 (route
// absent, or a GET on a POST-only route) is a clean false, not an error.
func (c *Client) managerGETOK(ctx context.Context, path string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = readBounded(resp.Body, maxManagerSmallBytes)
	return resp.StatusCode/100 == 2, nil
}

// managerVersion fetches Manager's version string for the detected line. V3
// serves it as PLAIN TEXT ("V3.41"), not JSON — verified live.
func (c *Client) managerVersion(ctx context.Context, line string) (string, error) {
	path := "/api/manager/version"
	if line == ManagerLineV4 || line == ManagerLineV4Legacy {
		path = "/api/v2/manager/version"
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, readErr := readBounded(resp.Body, maxManagerSmallBytes)
	if resp.StatusCode/100 != 2 {
		return "", statusError("manager version", resp.StatusCode, data)
	}
	if readErr != nil {
		return "", fmt.Errorf("read manager version: %w", readErr)
	}
	v := strings.TrimSpace(string(data))
	// Tolerate a JSON-quoted or {"version":…} shape from a future line.
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s, nil
		}
	}
	if strings.HasPrefix(v, "{") {
		var obj struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(v), &obj) == nil && obj.Version != "" {
			return obj.Version, nil
		}
	}
	if len(v) > 64 {
		v = v[:64]
	}
	return v, nil
}

// ManagerMappings fetches GET /api/customnode/getmappings — the class-name ->
// pack index (1.4 MB / 41,670 classes live).
//
// mode=cache is MANDATORY: the handler reads request.rel_url.query["mode"] with
// bracket access, so omitting it is a Python KeyError -> HTTP 500 (upstream
// issue #2740, reproduced live).
func (c *Client) ManagerMappings(ctx context.Context) (json.RawMessage, error) {
	return c.managerIndex(ctx, "getmappings", url.Values{"mode": {"cache"}})
}

// ManagerNodePacks fetches GET /api/customnode/getlist — the node-pack list
// (4.5 MB / 7358 packs live), which is the ONLY source of cnr_latest and
// therefore of Installable.
//
// mode=cache is mandatory for the same KeyError reason as getmappings;
// skip_update=true keeps Manager from doing a network refresh on our behalf.
//
// This endpoint is GONE in Manager V4's default mode. Its absence is reported as
// an error so the caller can fall back, but it must NOT be treated as fatal.
func (c *Client) ManagerNodePacks(ctx context.Context) (json.RawMessage, error) {
	return c.managerIndex(ctx, "getlist", url.Values{"mode": {"cache"}, "skip_update": {"true"}})
}

// managerIndex GETs one of the two large Manager index documents with a bounded
// read and a checked JSON shape.
func (c *Client) managerIndex(ctx context.Context, name string, q url.Values) (json.RawMessage, error) {
	path := "/api/customnode/" + name + "?" + q.Encode()
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("manager %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := readBounded(resp.Body, maxManagerSmallBytes)
		return nil, statusError("manager "+name, resp.StatusCode, data)
	}
	data, err := readBounded(resp.Body, maxManagerIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("read manager %s: %w", name, err)
	}
	// Validate the envelope now so a truncated/HTML body fails HERE rather than
	// silently becoming an empty attribution index later.
	if !json.Valid(data) {
		return nil, fmt.Errorf("manager %s: response is not valid JSON", name)
	}
	return json.RawMessage(data), nil
}

// ManagerInstalledDiff returns the node packs present on DISK but not in the set
// ComfyUI imported at startup — i.e. packs that landed but need a restart.
//
// This is the portable install-success signal, and it exists because the obvious
// ones do not work: V3 destroys `nodepack_result` immediately after its websocket
// broadcast and never exposes it over REST, and /manager/queue/status reports
// is_processing==false BEFORE you start, so a naive poll exits instantly and
// reports success for work that never ran. The diff works identically on V3 and
// V4 and needs no websocket.
//
//	GET /api/customnode/installed              -> live disk scan
//	GET /api/customnode/installed?mode=imported -> the startup-time snapshot
//
// A non-empty result means "installed, restart pending" — exactly the state the
// UX reports anyway. The returned slice is sorted.
func (c *Client) ManagerInstalledDiff(ctx context.Context) ([]string, error) {
	disk, err := c.managerInstalledSet(ctx, "")
	if err != nil {
		return nil, err
	}
	imported, err := c.managerInstalledSet(ctx, "imported")
	if err != nil {
		return nil, err
	}
	var pending []string
	for name := range disk {
		if _, ok := imported[name]; !ok {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return pending, nil
}

// managerInstalledSet fetches one side of the installed diff. mode "" is the live
// disk scan; "imported" is the startup-time snapshot.
func (c *Client) managerInstalledSet(ctx context.Context, mode string) (map[string]struct{}, error) {
	path := "/api/customnode/installed"
	if mode != "" {
		path += "?" + url.Values{"mode": {mode}}.Encode()
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("manager installed: %w", err)
	}
	defer resp.Body.Close()
	data, readErr := readBounded(resp.Body, maxManagerSmallBytes)
	if resp.StatusCode/100 != 2 {
		return nil, statusError("manager installed", resp.StatusCode, data)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read manager installed: %w", readErr)
	}
	// The value shape varies across Manager versions ({ver,cnr_id,aux_id,enabled}
	// today); only the KEYS are load-bearing, so decode the values as raw.
	var set map[string]json.RawMessage
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("parse manager installed: %w", err)
	}
	out := make(map[string]struct{}, len(set))
	for k := range set {
		out[k] = struct{}{}
	}
	return out, nil
}

// ManagerInstall asks ComfyUI-Manager to install pack, then starts its queue.
//
// It is DELEGATION, not execution: civitai-manager never writes to custom_nodes/
// and never runs pip. Manager applies its own security_level policy and can
// refuse — a refusal comes back as an error naming the status.
//
// Only a CNR-released pack is sent. A nightly-only pack routes Manager to its
// git-url path, which its default policy refuses; offering it would be a button
// that always fails, so the caller must gate on Pack.Installable.
//
// Wire details that will bite (all verified against the live server / its source):
//   - Content-Type MUST be application/json. Manager's
//     _reject_simple_form_content_type 400s text/plain — which is exactly what
//     Go's http.Post sends by default. Client.do sets the JSON header whenever a
//     body is present, and the no-body POSTs below send "{}" for that reason.
//   - V3's handler bracket-accesses json_data['version'], ['channel'] and
//     ['mode'], so those keys are REQUIRED, not optional.
//   - V3 needs a second call (queue/start) to actually run the queued task;
//     V4 queues and runs from the one envelope.
func (c *Client) ManagerInstall(ctx context.Context, info *ManagerInfo, pack Pack) error {
	if info == nil || !info.Present {
		return ErrManagerAbsent
	}
	if !pack.Installable {
		reason := pack.Reason
		if reason == "" {
			reason = "this pack has no Comfy Registry release"
		}
		return fmt.Errorf("comfy: refusing to install %q — %s", pack.Title, reason)
	}
	id := firstNonEmpty(pack.ID, pack.Title)
	version := firstNonEmpty(pack.Version, "latest")

	switch info.Line {
	case ManagerLineV4, ManagerLineV4Legacy:
		// V4 consolidated the per-operation routes into one envelope in 4.2.1,
		// specifically breaking third-party callers — hence the probe.
		path := "/api/v2/manager/queue/task"
		if info.Line == ManagerLineV4Legacy {
			path = "/api/v2/manager/queue/batch"
		}
		body := map[string]any{
			"ui_id":     NewID(),
			"client_id": NewID(),
			"kind":      "install",
			"params": map[string]any{
				"id":                id,
				"selected_version":  version,
				"version":           version,
				"repository":        pack.Repository,
				"channel":           "default",
				"mode":              "cache",
				"skip_post_install": false,
			},
		}
		return c.managerPostJSON(ctx, "manager install", path, body)

	case ManagerLineV3:
		body := map[string]any{
			"ui_id":            NewID(),
			"id":               id,
			"version":          version,
			"selected_version": version,
			"repository":       pack.Repository,
			"channel":          "default",
			"mode":             "cache",
			"files":            []string{pack.Repository},
		}
		if err := c.managerPostJSON(ctx, "manager install", "/api/manager/queue/install", body); err != nil {
			return err
		}
		// V3 queues but does not run; queue/start reads no body, so it is one of
		// the handlers guarded by _reject_simple_form_content_type — send "{}".
		return c.managerPostJSON(ctx, "manager queue start", "/api/manager/queue/start", struct{}{})

	default:
		return ErrManagerAbsent
	}
}

// managerPostJSON POSTs a JSON body and treats any 2xx as success. Manager's
// install-refused path answers 403/404 with a terse text body, which is surfaced
// verbatim so the user sees Manager's own reason.
func (c *Client) managerPostJSON(ctx context.Context, op, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: marshal body: %w", op, err)
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	data, _ := readBounded(resp.Body, maxManagerSmallBytes)
	if resp.StatusCode/100 != 2 {
		return statusError(op, resp.StatusCode, data)
	}
	return nil
}

// ManagerQueueStatus is Manager's own task-queue counters. NOTE that
// IsProcessing is false BEFORE a queue is started, so it can never be used alone
// as "the install finished" — use ManagerInstalledDiff for that.
type ManagerQueueStatus struct {
	TotalCount      int  `json:"total_count"`
	DoneCount       int  `json:"done_count"`
	InProgressCount int  `json:"in_progress_count"`
	IsProcessing    bool `json:"is_processing"`
}

// ManagerQueueStatus fetches Manager's task-queue counters for progress display.
func (c *Client) ManagerQueueStatus(ctx context.Context, info *ManagerInfo) (*ManagerQueueStatus, error) {
	if info == nil || !info.Present {
		return nil, ErrManagerAbsent
	}
	path := "/api/manager/queue/status"
	if info.Line == ManagerLineV4 || info.Line == ManagerLineV4Legacy {
		path = "/api/v2/manager/queue/status"
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("manager queue status: %w", err)
	}
	defer resp.Body.Close()
	data, readErr := readBounded(resp.Body, maxManagerSmallBytes)
	if resp.StatusCode/100 != 2 {
		return nil, statusError("manager queue status", resp.StatusCode, data)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read manager queue status: %w", readErr)
	}
	var st ManagerQueueStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse manager queue status: %w", err)
	}
	return &st, nil
}

// ComfyQueueBusy reports whether ComfyUI has running or pending prompts, by
// reading GET /api/queue. This is the guard in front of every restart.
func (c *Client) ComfyQueueBusy(ctx context.Context) (busy bool, running, pending int, err error) {
	resp, e := c.do(ctx, http.MethodGet, "/queue", nil)
	if e != nil {
		return false, 0, 0, fmt.Errorf("queue: %w", e)
	}
	defer resp.Body.Close()
	data, readErr := readBounded(resp.Body, maxJSONBytes)
	if resp.StatusCode/100 != 2 {
		return false, 0, 0, statusError("queue", resp.StatusCode, data)
	}
	if readErr != nil {
		return false, 0, 0, fmt.Errorf("read queue: %w", readErr)
	}
	var q struct {
		Running []json.RawMessage `json:"queue_running"`
		Pending []json.RawMessage `json:"queue_pending"`
	}
	if e := json.Unmarshal(data, &q); e != nil {
		return false, 0, 0, fmt.Errorf("parse queue: %w", e)
	}
	running, pending = len(q.Running), len(q.Pending)
	return running+pending > 0, running, pending, nil
}

// ManagerReboot restarts ComfyUI through ComfyUI-Manager, REFUSING while its
// queue is busy.
//
// 🔴 That refusal is the whole point. Manager's /manager/reboot calls os.execv
// with NO queue inspection whatsoever: it does not decline while a generation is
// running, it destroys it. So we read GET /api/queue ourselves first and return
// ErrQueueBusy when queue_running or queue_pending is non-empty. This is the most
// destructive call in the feature and must never be made automatically — only
// from an explicit user action.
//
// The process is replaced MID-REQUEST, so a transport error / EOF after the
// request was sent is SUCCESS, not failure — it is treated as such, and the
// caller should then poll ManagerAlive until ComfyUI answers again.
func (c *Client) ManagerReboot(ctx context.Context, info *ManagerInfo) error {
	if info == nil || !info.Present {
		return ErrManagerAbsent
	}
	busy, running, pending, err := c.ComfyQueueBusy(ctx)
	if err != nil {
		// Fail CLOSED: if we cannot prove the queue is idle, we do not reboot.
		return fmt.Errorf("%w: could not read the ComfyUI queue: %v", ErrQueueBusy, err)
	}
	if busy {
		return fmt.Errorf("%w: %d running, %d pending — wait for them to finish or cancel them first",
			ErrQueueBusy, running, pending)
	}

	path := "/api/manager/reboot"
	if info.Line == ManagerLineV4 || info.Line == ManagerLineV4Legacy {
		path = "/api/v2/manager/reboot"
	}
	// reboot reads no body, so it IS gated by _reject_simple_form_content_type:
	// send "{}" with the JSON content type.
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader([]byte("{}")))
	if err != nil {
		// os.execv replaced the process before it could answer. That is success.
		return nil
	}
	defer resp.Body.Close()
	data, _ := readBounded(resp.Body, maxManagerSmallBytes)
	if resp.StatusCode/100 != 2 {
		return statusError("manager reboot", resp.StatusCode, data)
	}
	return nil
}

// ManagerAlive reports whether Manager's version endpoint is answering. It is the
// readiness probe to poll after a reboot: ComfyUI is back once this returns true.
func (c *Client) ManagerAlive(ctx context.Context, info *ManagerInfo) bool {
	line := ManagerLineV3
	if info != nil && info.Line != ManagerLineNone {
		line = info.Line
	}
	_, err := c.managerVersion(ctx, line)
	return err == nil
}
