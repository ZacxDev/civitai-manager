package comfy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Outbound egress for custom-node attribution: the Comfy Registry and the static
// extension-node-map fallback. This is NEW egress to two hosts, read-only, and
// must be disclosed to the user the way `match_remote` is.
//
// Security posture is modelled directly on internal/hf/client.go (whose own guard
// reconstructs the civitai SDK's dialer, which lives in an unexported package):
//
//   - HTTPS-only on the initial request AND every redirect hop.
//   - A dial-time IP block on the RESOLVED address (DNS-rebind resistant):
//     loopback, unspecified, link-local (incl. 169.254.169.254), RFC1918, ULA,
//     CGNAT and multicast — applied to redirect hops too.
//   - An exact host allowlist (api.comfy.org + raw.githubusercontent.com),
//     enforced on every hop as an exact match, never a substring.
//   - The allowlist AND the post-DNS IP block BOTH apply, never either/or.
//   - A redirect cap and a bounded response body.
//
// The ComfyUI client in this package deliberately has NO such guard (it must
// reach loopback). These are different trust domains and must not share a client.

// Registry / fallback endpoints.
const (
	// registryHost is the Comfy Registry API host.
	registryHost = "api.comfy.org"
	// rawGitHubHost serves the static extension-node-map fallback.
	rawGitHubHost = "raw.githubusercontent.com"

	// registryBaseURL is the Comfy Registry origin.
	registryBaseURL = "https://" + registryHost

	// ExtensionNodeMapURL is the ONLY acceptable static fallback index URL.
	//
	// ⚠ node_db/legacy/ and node_db/forked/ answer `{}` with HTTP 200 — an
	// empty-but-successful body that reads as "no packs found". ⚠ The manager-v4
	// branch's in-repo copies are months stale; V4 fetches from main at runtime.
	// Both the V3 and V4 channel configs point at this `new` path.
	ExtensionNodeMapURL = "https://" + rawGitHubHost + "/Comfy-Org/ComfyUI-Manager/main/node_db/new/extension-node-map.json"
)

const (
	// registryUserAgent identifies this client to the Registry.
	registryUserAgent = "civitai-manager/nodepack-resolver"
	// maxRegistryRedirects caps the redirect chain. Neither endpoint redirects
	// today; the cap is defence in depth.
	maxRegistryRedirects = 5
	// maxRegistryJSONBytes bounds one Registry node lookup (a small object).
	maxRegistryJSONBytes = 4 << 20
	// maxExtensionNodeMapBytes bounds the static index (2.4 MB live).
	maxExtensionNodeMapBytes = 64 << 20
	// minExtensionNodeMapKeys is the "suspiciously empty" floor. A `{}` or
	// near-empty index is treated as a FETCH FAILURE, never as "no packs found" —
	// that distinction is what keeps a wrong node_db path from silently
	// unattributing every class.
	minExtensionNodeMapKeys = 100
	// registryConcurrency caps in-flight Registry lookups. There is NO batch
	// endpoint, so a workflow with N missing classes is N requests.
	registryConcurrency = 6
)

// Registry guard sentinels, so a guard rejection is distinguishable from an
// ordinary connection failure.
var (
	errRegistryBlockedIP     = errors.New("comfy: refusing to connect to a non-routable/private address")
	errRegistryBlockedHost   = errors.New("comfy: refusing to reach a non-allowlisted host")
	errRegistryBlockedScheme = errors.New("comfy: refusing a non-https request")
)

// ErrRegistryNotFound reports that the Comfy Registry has no pack for a class.
// It is a NORMAL "unattributed" outcome, not a failure: the 404 body is
// {"error":"","message":"No node found containing the specified ComfyUI node name"}.
var ErrRegistryNotFound = errors.New("comfy: no registry pack for this node class")

// RegistryClient is an SSRF-hardened HTTP client for api.comfy.org and
// raw.githubusercontent.com. The guard predicates are fields so tests can drive
// the real redirect/allowlist code paths against a local harness.
type RegistryClient struct {
	httpc *http.Client

	// registryBase / nodeMapURL are overridable for tests only.
	registryBase string
	nodeMapURL   string

	// hostOK reports whether a host may be dialed / redirected to.
	hostOK func(host string) bool
	// denyIP reports whether a RESOLVED dial IP must be refused.
	denyIP func(ip net.IP) bool

	// tlsClientConfig is a TEST-ONLY hook to trust a harness's self-signed cert.
	// Nil in production. It never relaxes the IP block or the redirect guard.
	tlsClientConfig *tls.Config
}

// NewRegistryClient builds the production hardened client. No credentials exist
// for either host — both endpoints are public and read-only.
func NewRegistryClient() *RegistryClient {
	c := &RegistryClient{
		registryBase: registryBaseURL,
		nodeMapURL:   ExtensionNodeMapURL,
		hostOK:       registryHostAllowed,
		denyIP:       isBlockedRegistryIP,
	}
	c.httpc = c.buildHTTPClient()
	return c
}

// buildHTTPClient wires the hardened transport (dial-time IP block) and the
// redirect guard (https-only + host allowlist + cap).
func (c *RegistryClient) buildHTTPClient() *http.Client {
	// Control runs after DNS resolution, with `address` = the RESOLVED ip:port,
	// for EACH candidate address, before the socket connects. Rejecting here is
	// DNS-rebind resistant: a hostname that resolves to a private IP is blocked
	// no matter what the allowlist said about the name.
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   c.dialControl,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		TLSClientConfig:       c.tlsClientConfig, // nil in production
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       60 * time.Second,
		CheckRedirect: c.checkRedirect,
	}
}

// dialControl refuses a resolved dial IP that is non-routable/private.
func (c *RegistryClient) dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The dialer resolves before Control, so a non-IP here is unexpected;
		// fail closed.
		return fmt.Errorf("%w: unresolved address %q", errRegistryBlockedIP, address)
	}
	if c.denyIP(ip) {
		return fmt.Errorf("%w: %s", errRegistryBlockedIP, ip)
	}
	return nil
}

// checkRedirect enforces the cap, https-only and the host allowlist on EVERY hop.
func (c *RegistryClient) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRegistryRedirects {
		return fmt.Errorf("comfy: stopped after %d redirects", maxRegistryRedirects)
	}
	if req.URL.Scheme != "https" {
		return errRegistryBlockedScheme
	}
	if !c.hostOK(req.URL.Hostname()) {
		return fmt.Errorf("%w: %s", errRegistryBlockedHost, req.URL.Hostname())
	}
	return nil
}

// registryHostAllowed is the allowlist: EXACTLY the two hosts this feature needs.
// Matching is exact (after lower-casing and stripping a trailing root dot), never
// a suffix or substring, so `api.comfy.org.evil.com` and `evil-api.comfy.org` are
// both rejected.
func registryHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == registryHost || host == rawGitHubHost
}

// isBlockedRegistryIP refuses loopback, unspecified, link-local (incl. the
// 169.254.169.254 metadata IP), private (RFC1918 / ULA fc00::/7), CGNAT
// (100.64.0.0/10), 0.0.0.0/8 and multicast, for IPv4 and IPv6 alike.
func isBlockedRegistryIP(ip net.IP) bool {
	if ip == nil {
		return true // fail closed
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// CGNAT 100.64.0.0/10 is not covered by IsPrivate.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 0.0.0.0/8 "this network".
		if v4[0] == 0 {
			return true
		}
	}
	return false
}

// registryNode is the subset of GET /comfy-nodes/{class}/node we consume. The
// top-level object describes the PACK that contains the class, not the class.
type registryNode struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Repository    string `json:"repository"`
	LatestVersion struct {
		Version string `json:"version"`
	} `json:"latest_version"`
}

// LookupClass asks the Comfy Registry which pack provides a node class.
//
// The class goes in a PATH SEGMENT and MUST be URL-escaped: "CR Float To Integer"
// becomes "CR%20Float%20To%20Integer" and "+" becomes "%2B" (url.PathEscape leaves
// "+" alone, which the server would read as a space, so it is escaped explicitly).
// Matching is CASE-SENSITIVE.
//
// A 404 is ErrRegistryNotFound — a normal "unattributed", not a failure.
func (c *RegistryClient) LookupClass(ctx context.Context, class string) (Pack, error) {
	class = strings.TrimSpace(class)
	if class == "" {
		return Pack{}, ErrRegistryNotFound
	}
	raw, err := c.LookupClassRaw(ctx, class)
	if err != nil {
		return Pack{}, err
	}
	return DecodeRegistryPack(raw, class)
}

// LookupClassRaw is LookupClass returning the exact response body, so a caller
// can persist it in the node-pack cache and re-decode it later without a second
// request. A 404 is ErrRegistryNotFound.
func (c *RegistryClient) LookupClassRaw(ctx context.Context, class string) ([]byte, error) {
	class = strings.TrimSpace(class)
	if class == "" {
		return nil, ErrRegistryNotFound
	}
	seg := strings.ReplaceAll(url.PathEscape(class), "+", "%2B")
	rawURL := c.registryBase + "/comfy-nodes/" + seg + "/node"
	return c.getBytes(ctx, rawURL, maxRegistryJSONBytes)
}

// DecodeRegistryPack turns a cached/fetched Registry body into a Pack for class.
// A body that identifies no pack (including the JSON null a cached MISS is
// stored as) is ErrRegistryNotFound.
func DecodeRegistryPack(raw []byte, class string) (Pack, error) {
	var node registryNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return Pack{}, fmt.Errorf("comfy: decode registry node %q: %w", class, err)
	}
	id := firstNonEmpty(node.ID, node.Name)
	if id == "" {
		// A 200 with no identifiable pack is as good as a miss.
		return Pack{}, ErrRegistryNotFound
	}
	return Pack{
		ID:         id,
		Title:      firstNonEmpty(node.Name, node.ID),
		Repository: node.Repository,
		Version:    node.LatestVersion.Version,
		Classes:    []string{class},
		Source:     SourceRegistry,
		// Installable is decided by ComfyUI-Manager's node-pack list (cnr_latest),
		// never by the Registry: the Registry knowing a pack does not mean the
		// user's Manager will accept installing it. MergePacks lifts a true from
		// the Manager-backed entry when both rungs found the same pack.
		Reason: "ComfyUI-Manager's node-pack list did not confirm a registry release for this pack, so it cannot be installed automatically. Install it by hand from the repository URL.",
	}, nil
}

// LookupClasses resolves many classes concurrently (capped), returning the packs
// it found and the classes it did not.
//
// There is NO batch endpoint, so this is N requests — the concurrency cap and the
// caller's cache are what keep that civil. A per-class failure that is not a 404
// is reported in errs but never aborts the others: partial attribution beats none.
func (c *RegistryClient) LookupClasses(ctx context.Context, classes []string) (packs []Pack, unresolved []string, errs []error) {
	classes = sortedUnique(classes)
	if len(classes) == 0 {
		return nil, nil, nil
	}
	type result struct {
		pack Pack
		ok   bool
		err  error
	}
	results := make([]result, len(classes))

	sem := make(chan struct{}, registryConcurrency)
	var wg sync.WaitGroup
	for i, cls := range classes {
		wg.Add(1)
		go func(i int, cls string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = result{err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			p, err := c.LookupClass(ctx, cls)
			switch {
			case err == nil:
				results[i] = result{pack: p, ok: true}
			case errors.Is(err, ErrRegistryNotFound):
				results[i] = result{} // a clean miss
			default:
				results[i] = result{err: err}
			}
		}(i, cls)
	}
	wg.Wait()

	// Collect in the sorted class order so the output is deterministic.
	for i, r := range results {
		switch {
		case r.ok:
			packs = append(packs, r.pack)
		case r.err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", classes[i], r.err))
			unresolved = append(unresolved, classes[i])
		default:
			unresolved = append(unresolved, classes[i])
		}
	}
	// Fold duplicate packs (several classes routinely map to one pack).
	return MergePacks(packs), unresolved, errs
}

// FetchExtensionNodeMap fetches the static extension-node-map index — the
// no-Manager attribution source. The result has the same shape as Manager's
// getmappings and feeds BuildIndex directly (with a nil getlist).
//
// A suspiciously empty index is a FAILURE, not "no packs found". That guard is
// the whole point: node_db/legacy/ and node_db/forked/ answer `{}` with HTTP 200,
// so an empty-but-successful body would otherwise silently unattribute every
// class in every workflow.
//
// Correction to the design doc, verified live: the `new` index DOES carry
// nodename_pattern entries (38 of them, the same set the live endpoint applies),
// and 100% of its keys are URLs — so the pattern rung and the URL leg of the join
// are BOTH required on this path, not only on the Manager path.
func (c *RegistryClient) FetchExtensionNodeMap(ctx context.Context) (json.RawMessage, error) {
	data, err := c.getBytes(ctx, c.nodeMapURL, maxExtensionNodeMapBytes)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("comfy: parse extension-node-map: %w", err)
	}
	if len(probe) < minExtensionNodeMapKeys {
		return nil, fmt.Errorf("comfy: extension-node-map came back with only %d entries — treating it as a fetch failure, not as 'no packs found' (the legacy/ and forked/ paths answer {} with HTTP 200)", len(probe))
	}
	return json.RawMessage(data), nil
}

// newRequest builds a guarded GET. The scheme and host are checked BEFORE the
// request is made, so an off-allowlist URL never reaches the network at all.
func (c *RegistryClient) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("comfy: parse url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: %s", errRegistryBlockedScheme, u.Scheme)
	}
	if !c.hostOK(u.Hostname()) {
		return nil, fmt.Errorf("%w: %s", errRegistryBlockedHost, u.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", registryUserAgent)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// getBytes fetches rawURL with a bounded body. A 404 is ErrRegistryNotFound.
func (c *RegistryClient) getBytes(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	req, err := c.newRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrRegistryNotFound
	}
	if resp.StatusCode/100 != 2 {
		snippet, _ := readBounded(resp.Body, 512)
		return nil, fmt.Errorf("comfy: GET %s returned HTTP %d: %s",
			rawURL, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	data, err := readBounded(resp.Body, max)
	if err != nil {
		return nil, fmt.Errorf("comfy: read %s: %w", rawURL, err)
	}
	return data, nil
}
