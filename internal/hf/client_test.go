package hf

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},             // loopback v4
		{"::1", true},                   // loopback v6
		{"0.0.0.0", true},               // unspecified
		{"169.254.169.254", true},       // link-local (cloud metadata)
		{"169.254.1.1", true},           // link-local
		{"10.0.0.1", true},              // RFC1918
		{"172.16.5.5", true},            // RFC1918
		{"192.168.1.1", true},           // RFC1918
		{"100.64.0.1", true},            // CGNAT
		{"100.127.255.1", true},         // CGNAT upper
		{"fc00::1", true},               // ULA
		{"fe80::1", true},               // link-local v6
		{"224.0.0.1", true},             // multicast
		{"::ffff:127.0.0.1", true},      // IPv4-mapped loopback
		{"8.8.8.8", false},              // public
		{"1.1.1.1", false},              // public
		{"93.184.216.34", false},        // public (example.com)
		{"2606:4700:4700::1111", false}, // public v6
		{"100.63.255.255", false},       // just below CGNAT
		{"100.128.0.1", false},          // just above CGNAT
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isBlockedIP(ip); got != c.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
	if !isBlockedIP(nil) {
		t.Error("isBlockedIP(nil) must fail closed (true)")
	}
}

func TestHostAllowed(t *testing.T) {
	cases := []struct {
		host string
		ok   bool
	}{
		{"huggingface.co", true},
		{"us.aws.cdn.hf.co", true},
		{"cdn-lfs.huggingface.co", true},
		{"cdn-lfs-us-1.huggingface.co", true},
		{"HUGGINGFACE.CO", true},  // case-insensitive
		{"huggingface.co.", true}, // trailing dot
		{"evilhf.co", false},      // NOT a .hf.co dotted suffix
		{"hf.co", false},          // bare parent, not the origin, no dotted suffix
		{"huggingface.co.evil.com", false},
		{"nothuggingface.co", false},
		{"example.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hostAllowed(c.host); got != c.ok {
			t.Errorf("hostAllowed(%q) = %v, want %v", c.host, got, c.ok)
		}
	}
}

func TestIsTokenHost(t *testing.T) {
	if !isTokenHost("huggingface.co") {
		t.Error("token must be allowed on the HF origin")
	}
	for _, h := range []string{"us.aws.cdn.hf.co", "cdn-lfs.huggingface.co", "example.com"} {
		if isTokenHost(h) {
			t.Errorf("token must NOT be sent to %q (only the HF origin)", h)
		}
	}
}

// TestDialControlBlocksPrivateIPs exercises the REAL dial guard wired into the
// hardened transport: the same func net.Dialer.Control calls per resolved IP.
func TestDialControlBlocksPrivateIPs(t *testing.T) {
	c := NewClient("")
	for _, addr := range []string{"127.0.0.1:443", "169.254.169.254:443", "10.0.0.1:443", "[::1]:443"} {
		if err := c.dialControl("tcp", addr, nil); err == nil {
			t.Errorf("dialControl(%q) = nil, want blocked", addr)
		}
	}
	if err := c.dialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("dialControl(public) = %v, want nil", err)
	}
}

// TestCheckRedirectPolicy verifies the redirect guard: cap, https-only, host
// allowlist, and the token DROP on a non-token (CDN) host with token retained on
// the HF origin.
func TestCheckRedirectPolicy(t *testing.T) {
	c := NewClient("secret")

	mkReq := func(rawurl string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawurl, nil)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		return req
	}

	// Redirect cap.
	via := make([]*http.Request, maxRedirects)
	if err := c.checkRedirect(mkReq("https://huggingface.co/x"), via); err == nil {
		t.Error("expected redirect-cap error")
	}
	// Non-https hop refused.
	if err := c.checkRedirect(mkReq("http://huggingface.co/x"), nil); err == nil {
		t.Error("expected non-https redirect refused")
	}
	// Off-allowlist host refused.
	if err := c.checkRedirect(mkReq("https://evil.example.com/x"), nil); err == nil {
		t.Error("expected off-allowlist redirect refused")
	}
	// Redirect to the CDN (allowlisted, non-token host): allowed, token DROPPED.
	cdnReq := mkReq("https://us.aws.cdn.hf.co/xet-bridge-us/abc")
	if err := c.checkRedirect(cdnReq, nil); err != nil {
		t.Fatalf("CDN redirect should be allowed: %v", err)
	}
	if cdnReq.Header.Get("Authorization") != "" {
		t.Error("Authorization must be DROPPED on the CDN redirect hop")
	}
	// Redirect that stays on the HF origin: token retained.
	hfReq := mkReq("https://huggingface.co/other")
	if err := c.checkRedirect(hfReq, nil); err != nil {
		t.Fatalf("HF-origin redirect should be allowed: %v", err)
	}
	if hfReq.Header.Get("Authorization") == "" {
		t.Error("Authorization must be retained on an HF-origin redirect")
	}
}

// TestNewRequestTokenScoping verifies the token is attached ONLY to the HF origin,
// never to the CDN, and that https-only + host-allowlist are enforced when building.
func TestNewRequestTokenScoping(t *testing.T) {
	c := NewClient("secret")
	ctx := context.Background()

	req, err := c.newRequest(ctx, "https://huggingface.co/api/models/Bingsu/adetailer")
	if err != nil {
		t.Fatalf("hf-origin request: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer secret" {
		t.Error("token must be attached to the HF origin request")
	}

	cdn, err := c.newRequest(ctx, "https://us.aws.cdn.hf.co/xet-bridge-us/abc")
	if err != nil {
		t.Fatalf("cdn request: %v", err)
	}
	if cdn.Header.Get("Authorization") != "" {
		t.Error("token must NOT be attached to a CDN request")
	}

	if _, err := c.newRequest(ctx, "http://huggingface.co/x"); err == nil {
		t.Error("non-https request must be refused")
	}
	if _, err := c.newRequest(ctx, "https://evil.example.com/x"); err == nil {
		t.Error("off-allowlist host must be refused")
	}
}

// newTLSHarness starts an https test server on the given loopback host and returns
// it. Binding B on 127.0.0.2 gives the redirect target a DISTINCT hostname from A
// (127.0.0.1) so the token-host predicate can differentiate them.
func newTLSHarness(t *testing.T, host string, h http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("cannot bind %s (env limitation): %v", host, err)
	}
	srv := &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: h},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// hardenedTestClient builds a client with the REAL hardened transport + redirect
// guard but test-friendly predicates: it trusts self-signed certs and (optionally)
// allows loopback so a local harness is reachable, while the guard under test stays
// real. tokenOrigin is the host the token may be sent to.
func hardenedTestClient(token, tokenOrigin string, allowLoopback bool) *Client {
	c := &Client{
		token:       token,
		hostOK:      func(string) bool { return true }, // reachability, not the guard under test
		tokenHostOK: func(h string) bool { return h == tokenOrigin },
		denyIP: func(ip net.IP) bool {
			if allowLoopback && ip.IsLoopback() {
				return false
			}
			return isBlockedIP(ip)
		},
		tlsClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test harness self-signed cert
	}
	c.httpc = c.buildHTTPClient()
	return c
}

// TestEndToEndTokenDropOnRedirect drives a real request through the hardened
// transport: server A (the "origin", token host) 302s to server B (the "CDN", a
// different loopback host). It asserts A receives the bearer token and B does NOT —
// the drop happens through the real CheckRedirect on an actual redirect hop.
func TestEndToEndTokenDropOnRedirect(t *testing.T) {
	var gotB string
	bReceived := make(chan struct{}, 1)
	srvB := newTLSHarness(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotB = r.Header.Get("Authorization")
		select {
		case bReceived <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	var gotA string
	srvA := newTLSHarness(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotA = r.Header.Get("Authorization")
		http.Redirect(w, r, srvB.URL+"/file", http.StatusFound)
	}))

	// Token origin is A's host (127.0.0.1); B is 127.0.0.2, a non-token host.
	c := hardenedTestClient("secret", "127.0.0.1", true)
	resp, err := c.DownloadFile(context.Background(), srvA.URL+"/resolve")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	defer resp.Body.Close()

	if gotA != "Bearer secret" {
		t.Errorf("origin A Authorization = %q, want Bearer secret", gotA)
	}
	if gotB != "" {
		t.Errorf("CDN B Authorization = %q, want empty (token dropped on redirect)", gotB)
	}
}

// TestEndToEndRedirectToPrivateIPBlocked drives a real request where the origin
// (reachable via the relaxed loopback allowance) 302s to the cloud-metadata IP; the
// dial to that private IP is refused by the REAL dial guard on the redirect hop.
func TestEndToEndRedirectToPrivateIPBlocked(t *testing.T) {
	srvA := newTLSHarness(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to the AWS metadata IP over https + an allowlisted-looking host is
		// simulated here by a raw IP URL; the redirect guard passes it (https), the
		// dial guard must refuse it.
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))

	c := hardenedTestClient("", "127.0.0.1", true) // loopback allowed so A is reachable; 169.254 still blocked
	resp, err := c.DownloadFile(context.Background(), srvA.URL+"/resolve")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the redirect to the private metadata IP to be refused")
	}
	if !strings.Contains(err.Error(), "non-routable") && !strings.Contains(err.Error(), "refusing to connect") {
		t.Errorf("error %v should indicate the dial guard blocked the private IP", err)
	}
}
