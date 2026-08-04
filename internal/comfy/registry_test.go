package comfy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- guard unit tests (mirrors internal/hf/client_test.go) ---

func TestIsBlockedRegistryIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"0.0.0.0", "::", // unspecified
		"169.254.169.254",                       // the cloud metadata IP
		"fe80::1",                               // link-local
		"10.0.0.1", "172.16.0.1", "192.168.1.1", // RFC1918
		"fd00::1",                       // ULA
		"100.64.0.1", "100.127.255.255", // CGNAT
		"224.0.0.1", "ff02::1", // multicast
		"0.1.2.3", // 0.0.0.0/8
	}
	for _, s := range blocked {
		if !isBlockedRegistryIP(net.ParseIP(s)) {
			t.Errorf("isBlockedRegistryIP(%s) = false, want blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111", "100.63.255.255", "100.128.0.1"}
	for _, s := range allowed {
		if isBlockedRegistryIP(net.ParseIP(s)) {
			t.Errorf("isBlockedRegistryIP(%s) = true, want allowed", s)
		}
	}
	if !isBlockedRegistryIP(nil) {
		t.Error("a nil IP must fail closed")
	}
}

// TestRegistryHostAllowed pins the allowlist to EXACTLY the two hosts, matched
// exactly — a suffix or substring match would accept api.comfy.org.evil.com.
func TestRegistryHostAllowed(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"api.comfy.org", true},
		{"API.Comfy.ORG", true},
		{"api.comfy.org.", true}, // trailing root dot
		{"raw.githubusercontent.com", true},
		{"api.comfy.org.evil.com", false},
		{"evil-api.comfy.org", false},
		{"comfy.org", false},
		{"githubusercontent.com", false},
		{"raw.githubusercontent.com.evil.com", false},
		{"huggingface.co", false},
		{"civitai.com", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := registryHostAllowed(tc.host); got != tc.want {
			t.Errorf("registryHostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestRegistryDialControlBlocksPrivateIPs(t *testing.T) {
	c := NewRegistryClient()
	for _, addr := range []string{"127.0.0.1:443", "169.254.169.254:443", "10.0.0.1:443", "[::1]:443", "100.64.0.1:443"} {
		if err := c.dialControl("tcp", addr, nil); err == nil {
			t.Errorf("dialControl(%q) = nil, want blocked", addr)
		}
	}
	if err := c.dialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("dialControl(public) = %v, want nil", err)
	}
}

// TestRegistryCheckRedirectPolicy: the cap, https-only and the allowlist apply on
// EVERY hop, not just the first request.
func TestRegistryCheckRedirectPolicy(t *testing.T) {
	c := NewRegistryClient()
	mkReq := func(rawurl string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawurl, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if err := c.checkRedirect(mkReq("https://api.comfy.org/x"), make([]*http.Request, maxRegistryRedirects)); err == nil {
		t.Error("expected a redirect-cap error")
	}
	if err := c.checkRedirect(mkReq("http://api.comfy.org/x"), nil); err == nil {
		t.Error("a non-https hop must be refused")
	}
	if err := c.checkRedirect(mkReq("https://evil.example.com/x"), nil); err == nil {
		t.Error("an off-allowlist hop must be refused")
	}
	if err := c.checkRedirect(mkReq("https://huggingface.co/x"), nil); err == nil {
		t.Error("another project's allowlisted host must still be refused here")
	}
	if err := c.checkRedirect(mkReq("https://raw.githubusercontent.com/x"), nil); err != nil {
		t.Errorf("an allowlisted https hop must be permitted: %v", err)
	}
}

// TestRegistryNewRequestGuards: an off-allowlist or non-https URL must be refused
// BEFORE it touches the network.
func TestRegistryNewRequestGuards(t *testing.T) {
	c := NewRegistryClient()
	ctx := context.Background()
	if _, err := c.newRequest(ctx, "http://api.comfy.org/x"); !errors.Is(err, errRegistryBlockedScheme) {
		t.Errorf("err = %v, want a blocked-scheme error", err)
	}
	if _, err := c.newRequest(ctx, "https://evil.example.com/x"); !errors.Is(err, errRegistryBlockedHost) {
		t.Errorf("err = %v, want a blocked-host error", err)
	}
	if _, err := c.newRequest(ctx, "https://127.0.0.1/x"); !errors.Is(err, errRegistryBlockedHost) {
		t.Errorf("a loopback URL must be refused by the allowlist: %v", err)
	}
	req, err := c.newRequest(ctx, "https://api.comfy.org/comfy-nodes/X/node")
	if err != nil {
		t.Fatalf("an allowlisted https URL must build: %v", err)
	}
	if req.Header.Get("User-Agent") == "" {
		t.Error("requests must carry a descriptive User-Agent")
	}
}

// hardenedRegistryTestClient uses the REAL transport + redirect guard, relaxing
// only TLS trust and the loopback IP block so a local harness is reachable. The
// guard under test stays real.
func hardenedRegistryTestClient(base string, allowLoopback bool) *RegistryClient {
	c := &RegistryClient{
		registryBase: base,
		nodeMapURL:   base + "/node_db/new/extension-node-map.json",
		hostOK:       func(string) bool { return true }, // reachability, not the guard under test
		denyIP: func(ip net.IP) bool {
			if allowLoopback && ip.IsLoopback() {
				return false
			}
			return isBlockedRegistryIP(ip)
		},
		tlsClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test harness self-signed cert
	}
	c.httpc = c.buildHTTPClient()
	return c
}

func newTLSRegistryHarness(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestRegistryEndToEndRedirectToPrivateIPBlocked drives a REAL request whose
// origin 302s at the cloud metadata IP; the dial guard must refuse the hop.
func TestRegistryEndToEndRedirectToPrivateIPBlocked(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	c := hardenedRegistryTestClient(srv.URL, true)

	_, err := c.LookupClassRaw(context.Background(), "AnyClass")
	if err == nil {
		t.Fatal("expected the redirect to the metadata IP to be refused")
	}
	if !strings.Contains(err.Error(), "non-routable") && !strings.Contains(err.Error(), "refusing to connect") {
		t.Errorf("err %v should name the dial guard", err)
	}
}

// TestRegistryEndToEndPlainHTTPRedirectBlocked: a downgrade to http must be
// refused on the redirect hop.
func TestRegistryEndToEndPlainHTTPRedirectBlocked(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"leaked"}`))
	}))
	t.Cleanup(plain.Close)

	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/node", http.StatusFound)
	}))
	c := hardenedRegistryTestClient(srv.URL, true)

	if _, err := c.LookupClassRaw(context.Background(), "AnyClass"); err == nil {
		t.Fatal("expected the https->http downgrade to be refused")
	}
}

// --- Registry lookup behaviour, against captured REAL bodies ---

func registryFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestRegistryLookupClassDecodesRealBody uses the REAL api.comfy.org response for
// MMAudioSampler — the class the static map cannot attribute and the Registry can.
func TestRegistryLookupClassDecodesRealBody(t *testing.T) {
	var gotPath string
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write(registryFixture(t, "nodepack_registry_node.json"))
	}))
	c := hardenedRegistryTestClient(srv.URL, true)

	raw, err := c.LookupClassRaw(context.Background(), "MMAudioSampler")
	if err != nil {
		t.Fatalf("LookupClassRaw: %v", err)
	}
	pack, err := DecodeRegistryPack(raw, "MMAudioSampler")
	if err != nil {
		t.Fatalf("DecodeRegistryPack: %v", err)
	}
	if gotPath != "/comfy-nodes/MMAudioSampler/node" {
		t.Errorf("path = %q", gotPath)
	}
	if pack.ID != "comfyui-mmaudio" {
		t.Errorf("ID = %q, want comfyui-mmaudio", pack.ID)
	}
	if pack.Source != SourceRegistry {
		t.Errorf("Source = %q, want %q", pack.Source, SourceRegistry)
	}
	if len(pack.Classes) != 1 || pack.Classes[0] != "MMAudioSampler" {
		t.Errorf("Classes = %v", pack.Classes)
	}
	// The Registry knowing a pack does NOT mean the user's Manager will install
	// it; only getlist's cnr_latest decides that.
	if pack.Installable {
		t.Error("a Registry hit alone must not claim Installable")
	}
	if pack.Reason == "" {
		t.Error("a non-installable pack must carry a Reason")
	}
}

// TestRegistryClassPathEscaping is the 🔴 URL-shape test: the class is a PATH
// SEGMENT, must be escaped, and is case-sensitive. Live, "CR Float To Integer"
// escapes to CR%20Float%20To%20Integer (and 404s, which is a normal miss).
func TestRegistryClassPathEscaping(t *testing.T) {
	tests := []struct {
		class    string
		wantPath string
	}{
		{"MMAudioSampler", "/comfy-nodes/MMAudioSampler/node"},
		{"CR Float To Integer", "/comfy-nodes/CR%20Float%20To%20Integer/node"},
		{"Pick From Batch (mtb)", "/comfy-nodes/Pick%20From%20Batch%20%28mtb%29/node"},
		{"A+B", "/comfy-nodes/A%2BB/node"},
		{"a/b", "/comfy-nodes/a%2Fb/node"},
		{"Node#1?x=2", "/comfy-nodes/Node%231%3Fx=2/node"},
		{"Läbel", "/comfy-nodes/L%C3%A4bel/node"},
	}
	for _, tc := range tests {
		t.Run(tc.class, func(t *testing.T) {
			var gotPath string
			srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.EscapedPath()
				_, _ = w.Write([]byte(`{"id":"p","name":"P"}`))
			}))
			c := hardenedRegistryTestClient(srv.URL, true)
			if _, err := c.LookupClassRaw(context.Background(), tc.class); err != nil {
				t.Fatalf("LookupClassRaw: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("escaped path = %q, want %q", gotPath, tc.wantPath)
			}
			// A path separator must never escape the segment.
			if strings.Count(gotPath, "/") != 3 {
				t.Errorf("class leaked out of its path segment: %q", gotPath)
			}
		})
	}
}

// TestRegistryNotFoundIsNormal: the 404 body is the real
// {"error":"","message":"No node found …"} and must read as "unattributed".
func TestRegistryNotFoundIsNormal(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(registryFixture(t, "nodepack_registry_notfound.json"))
	}))
	c := hardenedRegistryTestClient(srv.URL, true)

	_, err := c.LookupClassRaw(context.Background(), "ZZZ_unrelated_class_9f3")
	if !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("err = %v, want ErrRegistryNotFound", err)
	}
}

// TestRegistryPacksFanOut covers the N-request fan-out: mixed hits/misses,
// deterministic output, and per-class errors that do not abort the rest.
//
// It drives NodePackResolver.RegistryPacks — the PRODUCTION fan-out, and the only
// one. (It used to drive RegistryClient.LookupClasses, a second, cache-less copy of
// the same loop that no production path called; deleting it moved these properties
// onto the live path rather than losing them.) The resolver is built with a nil
// cache so every class goes to the network, which is what the request counting
// needs to observe.
//
// ⚠ It does NOT cover the concurrency cap, and this comment used to claim it did.
// The claim was false in a way no timing could expose: the fixture below issues 5
// requests against a cap of registryConcurrency (6), so an "in-flight never exceeds
// the cap" assertion could not fail even with the semaphore deleted. The cap has its
// own test now — TestRegistryPacksCapsConcurrentRequests — which forces real overlap
// instead of hoping for it.
func TestRegistryPacksFanOut(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "Boom"):
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "MMAudio"):
			_, _ = w.Write([]byte(`{"id":"comfyui-mmaudio","name":"comfyui-mmaudio","repository":"https://github.com/x/mmaudio"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"","message":"No node found containing the specified ComfyUI node name"}`))
		}
	}))
	r := resolverWith(srv, nil, time.Now())

	classes := []string{"MMAudioSampler", "MMAudioModelLoader", "Boom", "Note Plus (mtb)", "Label (rgthree)"}
	packs, unresolved, errs := r.RegistryPacks(context.Background(), classes)

	// Both MMAudio classes map to ONE pack, unioned.
	if len(packs) != 1 {
		t.Fatalf("packs = %+v, want 1 (both MMAudio classes fold into one pack)", packs)
	}
	if len(packs[0].Classes) != 2 {
		t.Errorf("Classes = %v, want both MMAudio classes", packs[0].Classes)
	}
	// A 500 on one class must not lose the others. The error names the class through
	// the request URL it carries ("…/comfy-nodes/Boom/node"), which is the only place
	// RegistryPacks records which class failed.
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Boom") {
		t.Errorf("errs = %v, want exactly the Boom failure", errs)
	}
	wantUnresolved := []string{"Boom", "Label (rgthree)", "Note Plus (mtb)"}
	if fmt.Sprint(unresolved) != fmt.Sprint(wantUnresolved) {
		t.Errorf("unresolved = %v, want %v (sorted, including the errored class)", unresolved, wantUnresolved)
	}
}

// TestRegistryPacksCapsConcurrentRequests is the REAL guard on registryConcurrency.
// The Comfy Registry has no batch endpoint, so a workflow with N missing classes
// costs N requests; the semaphore in RegistryPacks is the only thing keeping that
// civil against a third party.
//
// 🔴 Its predecessor was VACUOUS and shipped that way for a long time: it issued 5
// requests against a cap of 6 and asserted "max in-flight never exceeded the cap",
// which cannot fail at any timing. Deleting the semaphore left it green. So this one
// is built to fail, not to pass:
//
//   - It issues 2x the cap, so the surplus MUST queue behind the semaphore.
//   - Every handler BLOCKS until the test releases it, holding the window open
//     deterministically instead of relying on scheduling luck to make requests
//     overlap.
//   - Waiting for exactly registryConcurrency arrivals is the harness's POSITIVE
//     CONTROL: it proves the counter observes real simultaneity before any assertion
//     is read. A counter wired to nothing (or a fan-out that is secretly serial)
//     never reaches the count and fails HERE, loudly, rather than reporting a
//     reassuring "max in-flight = 1".
//   - The peak is read while every handler is still blocked, so it is the true
//     simultaneous maximum and not a sample.
//
// The two failure directions land in DIFFERENT places, which is worth knowing when
// you read a red: "fewer than the cap were ever simultaneous" fails at the positive
// control above, and "more than the cap were simultaneous" fails at the assertion at
// the bottom. By the time that assertion runs the control has already proved
// maxInflight >= want, so it is really a one-sided `peak > want` check — do not read
// its message as covering the serial case.
// awaitResolve waits for the RegistryPacks goroutine, BOUNDED. An unbounded <-done
// would convert one hung fan-out into `panic: test timed out`, which kills the whole
// internal/comfy binary and truncates every test after this one — turning a single
// local failure into an unreadable package-wide one. Unreachable in practice (both
// call sites are after close(release), and the client carries its own timeout), which
// is exactly why it is cheap to bound.
func awaitResolve(t *testing.T, done <-chan struct{}, where string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("RegistryPacks did not return within 60s (%s); failing this test alone "+
			"rather than letting the package time out", where)
	}
}

func TestRegistryPacksCapsConcurrentRequests(t *testing.T) {
	// 🔴 CALIBRATION GUARD. Both `want` and `total` derive from registryConcurrency,
	// so the expectation and the subject move together — the "calibrated to its own
	// constant" mode. Mutating registryConcurrency to 1 left this test GREEN while
	// making it meaningless: `want` becomes 1, the positive control degenerates to
	// "wait for a single arrival" (which proves no simultaneity at all), and `total`
	// drops to 2. Any value below 2 cannot express a concurrency contest, so refuse
	// to report a verdict instead of reporting a vacuous pass.
	//
	// This does NOT make the test immune to the shared derivation — a cap of 3 or 4
	// would still move both sides — but it does close the one value at which the test
	// stops being about concurrency. The hazard the test exists for (the semaphore
	// deleted, every class hitting the Registry at once) is caught at any cap >= 2,
	// and the blind direction is more-civil-not-less.
	if registryConcurrency < 2 {
		t.Fatalf("registryConcurrency = %d: this test derives both its barrier and its "+
			"expectation from that constant, so below 2 it cannot observe simultaneity "+
			"and its pass would mean nothing. Re-express the fixture with a literal "+
			"before lowering the cap.", registryConcurrency)
	}

	const want = registryConcurrency
	const total = registryConcurrency * 2 // the surplus has to queue

	var inflight, maxInflight int64
	var mu sync.Mutex
	release := make(chan struct{})
	arrived := make(chan struct{}, total) // buffered: a handler must never block here

	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&inflight, 1)
		mu.Lock()
		if n > maxInflight {
			maxInflight = n
		}
		mu.Unlock()
		arrived <- struct{}{}
		<-release // hold: nothing completes until the peak has been read
		atomic.AddInt64(&inflight, -1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"","message":"No node found containing the specified ComfyUI node name"}`))
	}))

	classes := make([]string, total)
	for i := range classes {
		classes[i] = fmt.Sprintf("CapClass%02d", i)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = resolverWith(srv, nil, time.Now()).RegistryPacks(context.Background(), classes)
	}()

	// POSITIVE CONTROL: block until the cap's worth of requests are genuinely
	// simultaneous. Reaching this proves the harness can observe overlap at all.
	for i := 0; i < want; i++ {
		select {
		case <-arrived:
		case <-time.After(30 * time.Second):
			close(release)
			awaitResolve(t, done, "after the positive control timed out")
			t.Fatalf("only %d of %d requests were ever simultaneously in flight — the "+
				"fan-out is not concurrent (or the cap was lowered below %d), so this "+
				"test cannot say anything about the cap", i, want, want)
		}
	}

	// Every handler is still blocked, so anything the cap wrongly admitted has had a
	// wide, quiet window to arrive and be counted. With the semaphore in place the
	// surplus cannot move at all; without it the remaining requests are already in
	// flight from goroutines that were spawned in a tight loop.
	time.Sleep(250 * time.Millisecond)

	mu.Lock()
	peak := maxInflight
	mu.Unlock()

	close(release)
	awaitResolve(t, done, "after the peak was read")

	if peak != want {
		t.Errorf("peak simultaneous Registry requests = %d, want exactly %d "+
			"(registryConcurrency): the semaphore in RegistryPacks is gone, so all %d "+
			"classes would hit the Comfy Registry at once. (A peak BELOW the cap cannot "+
			"reach this line — the positive control above has already proved at least %d "+
			"were simultaneous — so this only ever reports the cap being exceeded.)",
			peak, want, total, want)
	}
}

// TestRegistryPacksAreDeterministic: the fan-out is concurrent, so the output must
// be re-sorted rather than arriving in completion order.
func TestRegistryPacksAreDeterministic(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q}`, seg[1], seg[1])
	}))
	r := resolverWith(srv, nil, time.Now())

	in := []string{"Delta", "Alpha", "Charlie", "Bravo", "Echo"}
	var want string
	for i := 0; i < 6; i++ {
		packs, unresolved, _ := r.RegistryPacks(context.Background(), in)
		b, _ := json.Marshal(struct {
			P []Pack
			U []string
		}{packs, unresolved})
		if i == 0 {
			want = string(b)
			continue
		}
		if string(b) != want {
			t.Fatalf("run %d differs:\n%s\nvs\n%s", i, b, want)
		}
	}
}

// TestRegistryPacksBlankClassesIssueNoRequest: a class list that is entirely blank
// after trimming resolves to nothing WITHOUT touching the network. (Distinct from
// TestRegistryPacksEmptyInput, which passes a nil slice — this one proves the blanks
// are stripped rather than requested as empty path segments.)
func TestRegistryPacksBlankClassesIssueNoRequest(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	r := resolverWith(srv, nil, time.Now())
	packs, unresolved, errs := r.RegistryPacks(context.Background(), []string{"", "   "})
	if len(packs) != 0 || len(unresolved) != 0 || len(errs) != 0 {
		t.Errorf("packs=%v unresolved=%v errs=%v", packs, unresolved, errs)
	}
	if hits != 0 {
		t.Errorf("issued %d request(s) for an empty class list", hits)
	}
}

// --- the static extension-node-map fallback ---

// TestFetchExtensionNodeMapRejectsEmpty is the 🔴 empty-but-successful guard.
// node_db/legacy/ and node_db/forked/ answer `{}` with HTTP 200; treating that as
// "no packs found" would silently unattribute every class in every workflow.
func TestFetchExtensionNodeMapRejectsEmpty(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"the real legacy/forked answer", `{}`, true},
		{"suspiciously small", `{"a":[["X"],{}],"b":[["Y"],{}]}`, true},
		{"not JSON", `<html>404</html>`, true},
		{"a JSON array", `[]`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			c := hardenedRegistryTestClient(srv.URL, true)
			_, err := c.FetchExtensionNodeMap(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if err != nil && strings.Contains(tc.name, "legacy") &&
				!strings.Contains(err.Error(), "fetch failure") {
				t.Errorf("the empty-index error must say it is a FETCH failure, not 'no packs found': %v", err)
			}
		})
	}
}

// TestFetchExtensionNodeMapFeedsBuildIndex: the static index has the same shape as
// Manager's getmappings and drives BuildIndex with a nil getlist.
//
// Correction to the design doc, verified live: the `new` index DOES carry
// nodename_pattern entries, and 100% of its keys are URLs — so both the pattern
// rung and the URL leg of the join matter on this path too.
func TestFetchExtensionNodeMapFeedsBuildIndex(t *testing.T) {
	real := registryFixture(t, "nodepack_extension_node_map.json")

	// Pad to clear the "suspiciously empty" floor while keeping the real entries.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(real, &doc); err != nil {
		t.Fatal(err)
	}
	realKeys := len(doc)
	for i := 0; i < minExtensionNodeMapKeys; i++ {
		doc[fmt.Sprintf("https://github.com/pad/pack-%03d", i)] =
			json.RawMessage(fmt.Sprintf(`[["PadClass%03d"],{"title_aux":"pad-%03d"}]`, i, i))
	}
	padded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(padded)
	}))
	c := hardenedRegistryTestClient(srv.URL, true)

	got, err := c.FetchExtensionNodeMap(context.Background())
	if err != nil {
		t.Fatalf("FetchExtensionNodeMap: %v", err)
	}
	ix, err := BuildIndex(got, nil)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if realKeys == 0 {
		t.Fatal("the extension-node-map fixture is empty")
	}

	// Every real key in this file is a URL, so attribution here EXERCISES the URL
	// path: no getlist means no pack-id join is even possible.
	packs, unattributed := ix.Attribute([]string{"RIFEInterpolation"})
	if len(unattributed) != 0 || len(packs) != 1 {
		t.Fatalf("packs=%+v unattributed=%v", packs, unattributed)
	}
	if !strings.Contains(packs[0].Repository, "GACLove/ComfyUI-VFI") {
		t.Errorf("Repository = %q — the URL key must become the repository", packs[0].Repository)
	}
	if packs[0].Installable {
		t.Error("without getlist nothing can be Installable")
	}

	// The pattern rung must be live on this path too.
	patPacks, patUnattributed := ix.Attribute([]string{"Brand New Node (mtb)"})
	if len(patUnattributed) != 0 || len(patPacks) != 1 || patPacks[0].Source != SourcePattern {
		t.Errorf("the static index's nodename_pattern rung did not fire: packs=%+v unattributed=%v", patPacks, patUnattributed)
	}
}

// TestExtensionNodeMapURLIsTheNewPath pins the ONE acceptable path — legacy/ and
// forked/ answer {} with HTTP 200 and must never be used.
func TestExtensionNodeMapURLIsTheNewPath(t *testing.T) {
	if !strings.Contains(ExtensionNodeMapURL, "/node_db/new/") {
		t.Errorf("ExtensionNodeMapURL = %q, want the node_db/new path", ExtensionNodeMapURL)
	}
	for _, bad := range []string{"node_db/legacy", "node_db/forked", "manager-v4"} {
		if strings.Contains(ExtensionNodeMapURL, bad) {
			t.Errorf("ExtensionNodeMapURL must not use %q: %s", bad, ExtensionNodeMapURL)
		}
	}
	if !strings.HasPrefix(ExtensionNodeMapURL, "https://"+rawGitHubHost+"/") {
		t.Errorf("ExtensionNodeMapURL must be on the allowlisted raw host: %s", ExtensionNodeMapURL)
	}
}

// TestRegistryBodyIsBounded: a hostile endpoint streaming forever must not
// exhaust memory.
func TestRegistryBodyIsBounded(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := strings.Repeat("a", 1<<16)
		for i := 0; i < 128; i++ { // 8 MiB, over the 4 MiB lookup cap
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	c := hardenedRegistryTestClient(srv.URL, true)
	_, err := c.LookupClassRaw(context.Background(), "Huge")
	if err == nil {
		t.Fatal("expected an oversized-body error")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("err = %v, want ErrResponseTooLarge", err)
	}
}
