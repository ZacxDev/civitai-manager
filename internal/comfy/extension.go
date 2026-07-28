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
