// Package hf is a thin, SSRF-hardened client for the public HuggingFace Hub REST
// API plus a curated resolver that maps a ComfyUI model FILENAME to a downloadable
// HuggingFace file. It is the FALLBACK source used by the "Download & run" flow
// when CivitAI resolution finds no match.
//
// Security posture (mirrors the civitai SDK's dialer, whose SSRF guard lives in an
// unexported package and cannot be imported here — so it is reconstructed):
//
//   - HTTPS-only on the initial request AND every redirect hop.
//   - A dial-time IP block on the RESOLVED address (DNS-rebind resistant): loopback,
//     unspecified, link-local (incl. the 169.254.169.254 metadata IP), RFC1918,
//     ULA, CGNAT and multicast are refused — applied to redirect hops too.
//   - A host allowlist (huggingface.co + the Xet/LFS CDN redirect targets) enforced
//     as an exact dotted-suffix match, NOT a substring, on every hop.
//   - The host-allowlist AND the post-DNS IP-block BOTH apply (never OR): allowlisting
//     a hostname does not bypass the IP check.
//   - An optional bearer token is attached ONLY to the HuggingFace API/resolve origin
//     and is DROPPED on the CDN redirect hop (the signed CDN URL is self-authorizing).
package hf

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// DefaultBaseURL is the public HuggingFace Hub origin (API + /resolve).
const DefaultBaseURL = "https://huggingface.co"

// userAgent identifies this client to HuggingFace (they ask for a descriptive UA).
const userAgent = "civitai-manager/hf-fallback"

// maxRedirects caps the redirect chain the hardened client follows. The HF path is
// origin → one CDN redirect, well within this.
const maxRedirects = 10

// errBlockedIP is the sentinel a dial refuses a private/loopback/link-local target
// with. It is surfaced through the dial error so callers (and tests) can identify a
// guard rejection distinctly from an ordinary connection failure.
var errBlockedIP = errors.New("hf: refusing to connect to a non-routable/private address")

// errBlockedHost is returned by the redirect guard for an off-allowlist host.
var errBlockedHost = errors.New("hf: refusing to follow redirect to a non-allowlisted host")

// errBlockedScheme is returned by the redirect guard for a non-HTTPS hop.
var errBlockedScheme = errors.New("hf: refusing to follow non-https redirect")

// Client is a hardened HuggingFace client. Construct it with NewClient; the guard
// predicates are fields so tests can exercise the real redirect/token code paths
// against local harness servers while the production predicates are unit-tested
// directly.
type Client struct {
	httpc *http.Client
	base  string
	token string

	// hostOK reports whether a host may be dialed / redirected to (the allowlist).
	hostOK func(host string) bool
	// tokenHostOK reports whether the bearer token may be attached to a request to
	// this host (the API/resolve origin only — never the CDN).
	tokenHostOK func(host string) bool
	// denyIP reports whether a RESOLVED dial IP must be refused (the SSRF block).
	denyIP func(ip net.IP) bool
}

// NewClient builds a production hardened client. token may be empty (anonymous —
// the public models we care about serve unauthenticated); when set it is attached
// only to the HuggingFace origin.
func NewClient(token string) *Client {
	c := &Client{
		base:        DefaultBaseURL,
		token:       strings.TrimSpace(token),
		hostOK:      hostAllowed,
		tokenHostOK: isTokenHost,
		denyIP:      isBlockedIP,
	}
	c.httpc = c.buildHTTPClient()
	return c
}

// buildHTTPClient wires the hardened transport (dial-time IP block) and the redirect
// guard (https-only + host allowlist + cap + token-drop) using the Client's guard
// predicates.
func (c *Client) buildHTTPClient() *http.Client {
	// Control runs after DNS resolution, with `address` = the RESOLVED ip:port, for
	// EACH candidate address, before the socket connects. Rejecting here is therefore
	// DNS-rebind resistant: a hostname that resolves to a private IP is blocked no
	// matter what the allowlist said about the name.
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
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Minute, // whole-download budget; large weights are slow
		CheckRedirect: c.checkRedirect,
	}
}

// dialControl is the net.Dialer.Control hook: it refuses a resolved dial IP that is
// non-routable/private. address is the resolved "ip:port".
func (c *Client) dialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The dialer resolves before Control, so a non-IP here is unexpected; fail closed.
		return fmt.Errorf("%w: unresolved address %q", errBlockedIP, address)
	}
	if c.denyIP(ip) {
		return fmt.Errorf("%w: %s", errBlockedIP, ip)
	}
	return nil
}

// checkRedirect enforces, on every redirect hop: the cap, https-only, the host
// allowlist, AND drops the bearer token when leaving a token host (the CDN hop).
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("hf: stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return errBlockedScheme
	}
	if !c.hostOK(req.URL.Hostname()) {
		return fmt.Errorf("%w: %s", errBlockedHost, req.URL.Hostname())
	}
	// Drop the Authorization header on any hop to a non-token host (the signed CDN
	// URL is self-authorizing; forwarding a bearer token cross-host must never happen).
	if !c.tokenHostOK(req.URL.Hostname()) {
		req.Header.Del("Authorization")
	}
	return nil
}

// hostAllowed reports whether host is HuggingFace's API/resolve origin or a
// Xet/LFS CDN redirect target. Matching is exact for the origin and an exact
// DOTTED-SUFFIX (never a substring) for the CDN parents, so `evilhf.co` or
// `huggingface.co.evil.com` are rejected.
func hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	if host == "huggingface.co" {
		return true
	}
	// .hf.co covers us.aws.cdn.hf.co (the Xet bridge) and regional variants;
	// .huggingface.co covers legacy cdn-lfs*.huggingface.co LFS hosts.
	for _, suf := range []string{".hf.co", ".huggingface.co"} {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}

// isTokenHost reports whether the bearer token may be sent to host. ONLY the
// HuggingFace origin (API + /resolve) — never the CDN (*.hf.co) — so the token is
// dropped on the 302 → CDN hop.
func isTokenHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "huggingface.co"
}

// isBlockedIP reports whether ip must be refused at dial time. It blocks loopback,
// unspecified, link-local (incl. 169.254.169.254), private (RFC1918 / ULA fc00::/7),
// CGNAT (100.64.0.0/10), and multicast — for both IPv4 and IPv6 (IPv4-mapped v6 is
// normalized first).
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true // fail closed
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() {
		return true
	}
	// CGNAT 100.64.0.0/10 is not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil {
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
