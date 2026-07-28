package hf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxJSONBody caps how many bytes an API JSON response may be before we refuse it.
// The endpoints we call (search, model-info, tree) return small payloads; a huge
// body is a signal something is wrong (or hostile) and is not slurped into memory.
const maxJSONBody = 8 << 20 // 8 MiB

// newRequest builds a GET request for rawURL and attaches the bearer token ONLY
// when the target host is a token host (the HF origin). It never sends the token
// to the CDN or any other host.
func (c *Client) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: %s", errBlockedScheme, u.Scheme)
	}
	if !c.hostOK(u.Hostname()) {
		return nil, fmt.Errorf("%w: %s", errBlockedHost, u.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" && c.tokenHostOK(u.Hostname()) {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// getJSON fetches rawURL and decodes the JSON body into v. It enforces the size cap
// and treats a non-2xx as an error (with a short body snippet). A 404 is reported as
// ErrNotFound so callers can treat a missing repo/file as a miss, not a hard failure.
func (c *Client) getJSON(ctx context.Context, rawURL string, v any) error {
	req, err := c.newRequest(ctx, rawURL)
	if err != nil {
		return err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("hf: GET %s returned HTTP %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("hf: decode %s: %w", rawURL, err)
	}
	return nil
}

// DownloadFile fetches a HuggingFace /resolve URL and returns the streaming
// response, mirroring the civitai Downloader shape (*http.Response, error) so the
// existing atomic writer can consume resp.Body / resp.ContentLength. The hardened
// client follows the 302 → CDN redirect under the redirect guard (https-only, host
// allowlist, token dropped on the CDN hop). The caller MUST close resp.Body.
func (c *Client) DownloadFile(ctx context.Context, fileURL string) (*http.Response, error) {
	req, err := c.newRequest(ctx, fileURL)
	if err != nil {
		return nil, err
	}
	return c.httpc.Do(req)
}
