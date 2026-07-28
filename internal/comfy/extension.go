package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfyext"
)

// The civitai-manager ComfyUI helper's routes. They exist ONLY when the user has
// installed the helper (see internal/comfyext) and restarted ComfyUI — a stock
// ComfyUI answers both with 404, which is the negative feature-detection signal.
const (
	extPingPath = "/civitai-manager/ping"
	extOpenPath = "/civitai-manager/open"
)

// maxExtBytes bounds the helper's (tiny) JSON responses.
const maxExtBytes = 8 << 10

// maxExtAssetBytes bounds the helper's frontend script. The shipped script is
// well under 16 KiB; the cap is generous enough to survive it growing and small
// enough that a wrong URL answering with something huge is rejected rather than
// buffered.
const maxExtAssetBytes = 256 << 10

// ErrExtensionAbsent reports that this ComfyUI is not running the civitai-manager
// helper: the ping route is missing, unreachable, or answered by something that
// is not our helper. It is a NORMAL, expected outcome — the caller falls back to
// the honest "here is the path, open it from the Workflows menu" UI.
var ErrExtensionAbsent = errors.New("the civitai-manager ComfyUI helper is not installed on this ComfyUI")

// ExtensionInfo is the decoded /civitai-manager/ping response.
type ExtensionInfo struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

// ExtensionPing probes for the civitai-manager helper.
//
// It is deliberately strict: anything other than a 200 carrying a JSON body whose
// "tool" field is exactly our tool name counts as ABSENT, so a proxy error page,
// an unrelated route, or a truncated/garbage body can never be mistaken for a
// working helper. Bound the probe with a short context deadline at the call site
// — an unreachable ComfyUI must not stall the request that triggered it.
func (c *Client) ExtensionPing(ctx context.Context) (*ExtensionInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, extPingPath, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExtensionAbsent, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxExtBytes))
		return nil, fmt.Errorf("%w (ping returned %d)", ErrExtensionAbsent, resp.StatusCode)
	}
	data, err := readBounded(resp.Body, maxExtBytes)
	if err != nil {
		return nil, fmt.Errorf("%w (unreadable ping response)", ErrExtensionAbsent)
	}
	var info ExtensionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("%w (ping response is not JSON)", ErrExtensionAbsent)
	}
	if info.Tool != comfyext.ToolName {
		return nil, fmt.Errorf("%w (ping answered by %q)", ErrExtensionAbsent, info.Tool)
	}
	return &info, nil
}

// ExtensionAsset verifies the helper's FRONTEND half is actually being served.
//
// WHY THIS EXISTS (a live-caught bug): ComfyUI registers a custom node's python
// routes ONCE, at startup, and keeps the handlers in memory. Delete the helper
// directory and /civitai-manager/ping keeps answering 200 with our exact body
// until ComfyUI restarts — a "zombie" helper. The static asset route, by
// contrast, serves from DISK, so it 404s the moment the directory is gone. The
// frontend script is the half that does the actual work (?cm_open= handling and
// the websocket listener), so a helper is only USABLE when this returns nil.
//
// It is deliberately as strict as ExtensionPing: only a 200 whose (size-bounded)
// body contains our extension-name marker counts. Bound it with a short context
// deadline at the call site.
func (c *Client) ExtensionAsset(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, comfyext.AssetURLPath, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrExtensionAbsent, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxExtAssetBytes))
		return fmt.Errorf("%w (its frontend script returned %d — ComfyUI probably needs a restart)", ErrExtensionAbsent, resp.StatusCode)
	}
	data, err := readBounded(resp.Body, maxExtAssetBytes)
	if err != nil {
		return fmt.Errorf("%w (its frontend script is unreadable: %v)", ErrExtensionAbsent, err)
	}
	if !bytes.Contains(data, []byte(comfyext.AssetMarker)) {
		return fmt.Errorf("%w (the script served at %s is not the civitai-manager helper)", ErrExtensionAbsent, comfyext.AssetURLPath)
	}
	return nil
}

// ExtensionOpen asks the helper to broadcast an "open this workflow" websocket
// event, so an ALREADY-OPEN ComfyUI tab jumps to relPath without a page reload or
// a duplicate tab. relPath is relative to the workflows directory (e.g.
// "civitai-manager/portrait-7.json") and is validated here as well as by the
// helper: it must be relative and contained.
//
// A missing helper surfaces as ErrExtensionAbsent, so the caller can fall back.
func (c *Client) ExtensionOpen(ctx context.Context, relPath string) error {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" || strings.HasPrefix(relPath, "/") || strings.Contains(relPath, `\`) {
		return fmt.Errorf("open workflow: %q must be a relative workflow path", relPath)
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".." {
			return fmt.Errorf("open workflow: %q must not escape the workflows directory", relPath)
		}
	}
	body, err := json.Marshal(map[string]string{"path": relPath})
	if err != nil {
		return fmt.Errorf("open workflow: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, extOpenPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrExtensionAbsent, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxExtBytes))
		return ErrExtensionAbsent
	}
	if resp.StatusCode/100 != 2 {
		data, _ := readBounded(resp.Body, maxExtBytes)
		return statusError("open workflow", resp.StatusCode, data)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxExtBytes))
	return nil
}
