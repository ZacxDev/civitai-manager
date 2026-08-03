package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	g "maragu.dev/gomponents"
)

// The `resolve_node_packs` opt-out. Custom-node attribution reaches TWO public
// hosts (api.comfy.org and raw.githubusercontent.com) to place the node classes a
// local ComfyUI-Manager could not. That egress has an opt-out, and "off" here has
// to mean NO REQUEST — not "a request whose answer we ignore".
//
// So the assertions below are network-level, not value-level: a DNS tripwire
// counts every hostname lookup the test binary attempts. Reaching either host
// requires resolving its name first, so a zero count is proof no connection was
// even attempted, and the ON case (which must trip the wire) proves the wire is
// armed and reachable rather than vacuously quiet.

// armDNSTripwire forces net.DefaultResolver onto the pure-Go path and replaces
// its dial hook with a counter that ALWAYS fails. Nothing can leave the machine
// while it is armed: every lookup is refused, so the "enabled" control exercises
// the real egress code path without making a real request.
//
// Literal-IP dials (httptest servers, the loopback ComfyUI/Manager URLs used by
// these tests) never consult a resolver, so the count is specific to the public
// hosts this feature would reach.
func armDNSTripwire(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	lookups := 0
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			mu.Lock()
			lookups++
			mu.Unlock()
			return nil, errors.New("dns refused by the test tripwire")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = prev })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return lookups
	}
}

// managerWithMapping is a ComfyUI-Manager that can place exactly one class. It
// stands in for the loopback rung, which `resolve_node_packs` must never gate.
func managerWithMapping() managerClient {
	return &fakeManager{
		info: managerPresent(),
		mappings: json.RawMessage(`{
			"comfy-mtb": [["Pick From Batch (mtb)"], {"title_aux": "comfy-mtb"}]
		}`),
		getlist: json.RawMessage(`{"node_packs": {
			"comfy-mtb": {"cnr_latest": "0.5.4", "repository": "https://github.com/melMass/comfy_mtb", "title": "comfy-mtb"}
		}}`),
	}
}

// TestNodePackResolverGatedAtConstruction pins the opt-out at the point the
// outbound client is BUILT. The resolver owns the only hardened HTTP client for
// the two public hosts, so "nil resolver" is the same statement as "no socket".
func TestNodePackResolverGatedAtConstruction(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		wantNil bool
	}{
		{name: "resolve_node_packs on builds the egress resolver", enabled: true, wantNil: false},
		{name: "resolve_node_packs off builds nothing", enabled: false, wantNil: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			srv.cfg.ResolveNodePacks = c.enabled
			got := srv.nodePackResolver()
			if (got == nil) != c.wantNil {
				t.Fatalf("nodePackResolver() nil = %v, want nil = %v", got == nil, c.wantNil)
			}
		})
	}
}

// TestAttributeDisabledMakesNoOutboundRequest is the important one: with the
// opt-out off and NO ComfyUI-Manager to fall back on, attribution must attempt
// ZERO name lookups — proven by the tripwire, not by the returned value — and
// must report the class as unattributed rather than erroring.
//
// The enabled sub-case is the positive control: the identical call with the flag
// on DOES trip the wire, so a zero count in the disabled case cannot be an
// artefact of a tripwire that never fires.
func TestAttributeDisabledMakesNoOutboundRequest(t *testing.T) {
	cases := []struct {
		name         string
		enabled      bool
		wantLookups  bool
		wantResolver bool
	}{
		{name: "enabled — the egress path is really exercised", enabled: true, wantLookups: true, wantResolver: true},
		{name: "disabled — nothing is looked up", enabled: false, wantLookups: false, wantResolver: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			srv.cfg.ResolveNodePacks = c.enabled
			// No ComfyUI at all: the Manager rung is skipped entirely, so the two
			// outbound rungs are the ONLY thing that could place this class.
			srv.cfg.ComfyURL = ""
			srv.managerClientFn = nil

			// Count how often the production resolver factory is consulted. It is the
			// sole owner of the outbound HTTP client, so zero calls is a second,
			// structural proof that no request could have been issued.
			built := 0
			srv.nodePackResolverFn = func() *comfy.NodePackResolver {
				if !srv.cfg.ResolveNodePacks {
					return nil
				}
				built++
				return comfy.NewNodePackResolver(srv.store)
			}

			lookups := armDNSTripwire(t)
			attr := srv.attributeMissingNodes(context.Background(), []string{"SomeUnplaceableNode"})

			if got := lookups(); (got > 0) != c.wantLookups {
				t.Errorf("DNS lookups = %d, want %s", got,
					map[bool]string{true: "at least one", false: "exactly zero"}[c.wantLookups])
			}
			if (built > 0) != c.wantResolver {
				t.Errorf("egress resolver built %d times, want built = %v", built, c.wantResolver)
			}
			if len(attr.Packs) != 0 {
				t.Errorf("nothing can be attributed with every lookup failing, got %+v", attr.Packs)
			}
			if len(attr.Unattributed) != 1 || attr.Unattributed[0] != "SomeUnplaceableNode" {
				t.Errorf("the class must degrade to unattributed, got %v", attr.Unattributed)
			}
		})
	}
}

// TestAttributeDisabledStillUsesLocalManager proves the opt-out is scoped to the
// two PUBLIC hosts. ComfyUI-Manager is on loopback, so with the lookups off a
// user who has Manager installed still gets full attribution — including the
// installable flag — and still makes no name lookup.
func TestAttributeDisabledStillUsesLocalManager(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.ResolveNodePacks = false
	srv.managerClientFn = managerWithMapping
	// No resolver seam here on purpose: this exercises the PRODUCTION gate, so the
	// only thing keeping the two public hosts out of it is the config value.

	lookups := armDNSTripwire(t)
	attr := srv.attributeMissingNodes(context.Background(),
		[]string{"Pick From Batch (mtb)", "SomeUnplaceableNode"})

	if got := lookups(); got != 0 {
		t.Errorf("DNS lookups = %d, want 0 — Manager is loopback and the lookups are off", got)
	}
	if !attr.ManagerPresent {
		t.Fatal("the Manager rung must still run with the online lookups off")
	}
	if len(attr.Packs) != 1 || attr.Packs[0].Title != "comfy-mtb" || !attr.Packs[0].Installable {
		t.Fatalf("Manager should still attribute an installable comfy-mtb, got %+v", attr.Packs)
	}
	// The class Manager could not place stays unattributed instead of going out.
	if len(attr.Unattributed) != 1 || attr.Unattributed[0] != "SomeUnplaceableNode" {
		t.Errorf("unattributed = %v, want only the class Manager could not place", attr.Unattributed)
	}
}

// TestNodePackEgressNoticeMatchesState pins the panel's disclosure to reality: it
// may only name the two hosts when they will actually be contacted, and when the
// lookups are off it must say so and name the key that turns them back on.
func TestNodePackEgressNoticeMatchesState(t *testing.T) {
	cases := []struct {
		name   string
		remote bool
		want   []string
		absent []string
	}{
		{
			name:   "on — the hosts are disclosed",
			remote: true,
			want: []string{
				// The apostrophe is HTML-escaped in the rendered fragment.
				"ComfyUI-Manager&#39;s local index",
				"api.comfy.org",
				"raw.githubusercontent.com",
				"resolve_node_packs: false",
			},
		},
		{
			name:   "off — no egress may be claimed",
			remote: false,
			want: []string{
				"local index only",
				"turned off",
				"resolve_node_packs: false",
				"nothing was sent off this machine",
			},
			absent: []string{"are looked up against"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The egress disclosure is PROVENANCE and now renders inside the failure
			// report's "Technical details" disclosure rather than at the top of the
			// missing-nodes panel. What it must say is unchanged and is still pinned
			// here — only the surface it is read from moved.
			attr := nodeAttribution{ManagerPresent: true, RemoteLookup: c.remote,
				Unattributed: []string{"SomeUnplaceableNode"}}
			body := renderString(t, g.Group(nodePackProvenanceNotes(attr)))
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q in:\n%s", w, body)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(body, a) {
					t.Errorf("must NOT contain %q in:\n%s", a, body)
				}
			}
		})
	}
}
