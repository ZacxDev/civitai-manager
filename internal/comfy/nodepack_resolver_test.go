package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memCache is an in-memory NodePackCache with a settable clock, so freshness and
// fail-open behaviour are testable without a database.
type memCache struct {
	mu      sync.Mutex
	entries map[string]memEntry
	// getErr / putErr force a broken cache.
	getErr error
	putErr error
	// puts counts successful writes.
	puts int64
}

type memEntry struct {
	raw       []byte
	fetchedAt time.Time
}

func newMemCache() *memCache { return &memCache{entries: map[string]memEntry{}} }

func (m *memCache) GetNodePackRaw(source string) ([]byte, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, time.Time{}, m.getErr
	}
	e, ok := m.entries[source]
	if !ok {
		return nil, time.Time{}, nil
	}
	return e.raw, e.fetchedAt, nil
}

func (m *memCache) PutNodePackRaw(source string, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.entries[source] = memEntry{raw: append([]byte(nil), raw...), fetchedAt: time.Now()}
	atomic.AddInt64(&m.puts, 1)
	return nil
}

func (m *memCache) seed(source string, raw []byte, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[source] = memEntry{raw: raw, fetchedAt: at}
}

func (m *memCache) has(source string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[source]
	return ok
}

// bigIndex builds a valid extension-node-map body over the "suspiciously empty"
// floor, with a marker entry so a test can tell two bodies apart.
func bigIndex(marker string) []byte {
	doc := map[string]any{
		"https://github.com/melMass/comfy_mtb": []any{
			[]string{"Pick From Batch (mtb)"},
			map[string]string{"title_aux": marker, "nodename_pattern": `\(mtb\)$`},
		},
	}
	for i := 0; i < minExtensionNodeMapKeys; i++ {
		doc[fmt.Sprintf("https://github.com/pad/pack-%03d", i)] = []any{
			[]string{fmt.Sprintf("PadClass%03d", i)},
			map[string]string{"title_aux": fmt.Sprintf("pad-%03d", i)},
		}
	}
	b, _ := json.Marshal(doc)
	return b
}

func indexMarker(t *testing.T, raw []byte) string {
	t.Helper()
	ix, err := BuildIndex(json.RawMessage(raw), nil)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	packs, _ := ix.Attribute([]string{"Pick From Batch (mtb)"})
	if len(packs) != 1 {
		t.Fatalf("packs = %+v", packs)
	}
	return packs[0].Title
}

// resolverWith builds a resolver whose registry client points at srv, with a
// fixed clock.
func resolverWith(srv *httptest.Server, cache NodePackCache, now time.Time) *NodePackResolver {
	return &NodePackResolver{
		Registry: hardenedRegistryTestClient(srv.URL, true),
		Cache:    cache,
		now:      func() time.Time { return now },
	}
}

// TestStaticIndexServesFreshCacheWithoutFetching: a fresh entry must not touch
// the network at all.
func TestStaticIndexServesFreshCacheWithoutFetching(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write(bigIndex("from-network"))
	}))

	now := time.Now()
	cache := newMemCache()
	cache.seed(cacheSourceExtensionNodeMap, bigIndex("from-cache"), now.Add(-time.Hour))

	r := resolverWith(srv, cache, now)
	got, err := r.StaticIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := indexMarker(t, got); m != "from-cache" {
		t.Errorf("served %q, want the cached copy", m)
	}
	if hits != 0 {
		t.Errorf("a fresh cache entry issued %d network request(s)", hits)
	}
}

// TestStaticIndexRefetchesWhenStale: past the TTL, the network wins and the cache
// is refreshed.
func TestStaticIndexRefetchesWhenStale(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bigIndex("from-network"))
	}))

	now := time.Now()
	cache := newMemCache()
	cache.seed(cacheSourceExtensionNodeMap, bigIndex("from-cache"), now.Add(-48*time.Hour))

	r := resolverWith(srv, cache, now)
	got, err := r.StaticIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := indexMarker(t, got); m != "from-network" {
		t.Errorf("served %q, want the refetched copy", m)
	}
	// And the refetched body replaced the stale one.
	cached, _, _ := cache.GetNodePackRaw(cacheSourceExtensionNodeMap)
	if m := indexMarker(t, cached); m != "from-network" {
		t.Errorf("cache holds %q, want the refetched copy", m)
	}
}

// TestStaticIndexFailsOpenToStale is the 🔴 fail-open contract: when the fetch
// fails and a stale copy exists, serve the stale copy. An out-of-date
// attribution beats no attribution.
func TestStaticIndexFailsOpenToStale(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"upstream 500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}},
		{"the empty legacy/forked answer", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}},
		{"garbage body", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html>nope</html>`))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTLSRegistryHarness(t, tc.handler)
			now := time.Now()
			cache := newMemCache()
			cache.seed(cacheSourceExtensionNodeMap, bigIndex("from-cache"), now.Add(-48*time.Hour))

			r := resolverWith(srv, cache, now)
			got, err := r.StaticIndex(context.Background())
			if err != nil {
				t.Fatalf("must fail open to the stale copy, got %v", err)
			}
			if m := indexMarker(t, got); m != "from-cache" {
				t.Errorf("served %q, want the stale cached copy", m)
			}
			// A failed fetch must NEVER poison the cache.
			cached, _, _ := cache.GetNodePackRaw(cacheSourceExtensionNodeMap)
			if m := indexMarker(t, cached); m != "from-cache" {
				t.Errorf("the failed fetch overwrote the cache with %q", m)
			}
		})
	}
}

// TestStaticIndexErrorsWithNothingCached: fail-open needs something to fall back
// to; with an empty cache the fetch error must surface.
func TestStaticIndexErrorsWithNothingCached(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	r := resolverWith(srv, newMemCache(), time.Now())
	if _, err := r.StaticIndex(context.Background()); err == nil {
		t.Fatal("expected the fetch error to surface")
	}
}

// TestResolverWorksWithoutACache: a nil cache is valid — every call goes to the
// network.
func TestResolverWorksWithoutACache(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write(bigIndex("from-network"))
	}))
	r := resolverWith(srv, nil, time.Now())
	for i := 0; i < 2; i++ {
		if _, err := r.StaticIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2 (no cache means no reuse)", hits)
	}
}

// TestResolverToleratesABrokenCache: a failing cache degrades to the network, it
// never fails attribution.
func TestResolverToleratesABrokenCache(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bigIndex("from-network"))
	}))
	cache := newMemCache()
	cache.getErr = errors.New("database is locked")
	cache.putErr = errors.New("attempt to write a readonly database")

	r := resolverWith(srv, cache, time.Now())
	got, err := r.StaticIndex(context.Background())
	if err != nil {
		t.Fatalf("a broken cache must not break attribution: %v", err)
	}
	if m := indexMarker(t, got); m != "from-network" {
		t.Errorf("served %q", m)
	}
}

// TestRegistryPacksCachesPerClass: the Registry has NO batch endpoint, so caching
// each class is what stops a re-render from re-issuing N requests. A cached MISS
// counts as an answer.
func TestRegistryPacksCachesPerClass(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if strings.Contains(r.URL.Path, "MMAudio") {
			_, _ = w.Write([]byte(`{"id":"comfyui-mmaudio","name":"comfyui-mmaudio","repository":"https://github.com/kijai/ComfyUI-MMAudio"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"","message":"No node found containing the specified ComfyUI node name"}`))
	}))

	cache := newMemCache()
	r := resolverWith(srv, cache, time.Now())
	classes := []string{"MMAudioSampler", "MMAudioModelLoader", "Note Plus (mtb)"}

	packs, unresolved, errs := r.RegistryPacks(context.Background(), classes)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(packs) != 1 || len(packs[0].Classes) != 2 {
		t.Fatalf("packs = %+v", packs)
	}
	if len(unresolved) != 1 || unresolved[0] != "Note Plus (mtb)" {
		t.Errorf("unresolved = %v", unresolved)
	}
	first := atomic.LoadInt64(&hits)
	if first != 3 {
		t.Fatalf("first pass issued %d requests, want 3", first)
	}
	// The MISS must be cached too, or every render re-asks for it.
	if !cache.has(cacheSourceRegistryPrefix + "Note Plus (mtb)") {
		t.Error("a Registry miss was not cached — it will be refetched on every render")
	}

	// Second pass: everything comes from cache, zero requests.
	packs2, unresolved2, errs2 := r.RegistryPacks(context.Background(), classes)
	if atomic.LoadInt64(&hits) != first {
		t.Errorf("second pass issued %d extra request(s)", atomic.LoadInt64(&hits)-first)
	}
	if len(errs2) != 0 {
		t.Fatalf("errs = %v", errs2)
	}
	a, _ := json.Marshal(struct {
		P []Pack
		U []string
	}{packs, unresolved})
	b, _ := json.Marshal(struct {
		P []Pack
		U []string
	}{packs2, unresolved2})
	if string(a) != string(b) {
		t.Errorf("the cached pass differs from the network pass:\n%s\nvs\n%s", b, a)
	}
}

// TestRegistryPacksFailsOpenToStalePerClass: one class's network failure falls
// back to its own stale entry and never costs the other classes their answer.
func TestRegistryPacksFailsOpenToStalePerClass(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "Flaky") {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":"comfyui-mmaudio","name":"comfyui-mmaudio"}`))
	}))

	now := time.Now()
	cache := newMemCache()
	// A STALE entry for the class whose refetch will fail.
	cache.seed(cacheSourceRegistryPrefix+"FlakyClass",
		[]byte(`{"id":"stale-pack","name":"stale-pack"}`), now.Add(-30*24*time.Hour))

	r := resolverWith(srv, cache, now)
	packs, unresolved, errs := r.RegistryPacks(context.Background(), []string{"FlakyClass", "MMAudioSampler"})

	if len(errs) != 0 {
		t.Errorf("a stale fallback must not report an error: %v", errs)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %v", unresolved)
	}
	var sawStale, sawFresh bool
	for _, p := range packs {
		switch p.ID {
		case "stale-pack":
			sawStale = true
		case "comfyui-mmaudio":
			sawFresh = true
		}
	}
	if !sawStale {
		t.Error("the failing class did not fall back to its stale entry")
	}
	if !sawFresh {
		t.Error("the healthy class lost its answer")
	}
}

// TestRegistryPacksReportsErrorWithNoStale: with nothing cached, a failure is
// reported and the class is unresolved — but the other classes still resolve.
func TestRegistryPacksReportsErrorWithNoStale(t *testing.T) {
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "Flaky") {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"id":"comfyui-mmaudio","name":"comfyui-mmaudio"}`))
	}))
	r := resolverWith(srv, newMemCache(), time.Now())

	packs, unresolved, errs := r.RegistryPacks(context.Background(), []string{"FlakyClass", "MMAudioSampler"})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one", errs)
	}
	if len(unresolved) != 1 || unresolved[0] != "FlakyClass" {
		t.Errorf("unresolved = %v", unresolved)
	}
	if len(packs) != 1 || packs[0].ID != "comfyui-mmaudio" {
		t.Errorf("packs = %+v — one failure must not lose the others", packs)
	}
}

// TestRegistryPacksExpiredCacheRefetches: past the TTL the entry is refetched.
func TestRegistryPacksExpiredCacheRefetches(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"id":"fresh-pack","name":"fresh-pack"}`))
	}))
	now := time.Now()
	cache := newMemCache()
	cache.seed(cacheSourceRegistryPrefix+"SomeClass",
		[]byte(`{"id":"old-pack","name":"old-pack"}`), now.Add(-30*24*time.Hour))

	r := resolverWith(srv, cache, now)
	packs, _, _ := r.RegistryPacks(context.Background(), []string{"SomeClass"})
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (the entry was past its TTL)", hits)
	}
	if len(packs) != 1 || packs[0].ID != "fresh-pack" {
		t.Errorf("packs = %+v", packs)
	}
}

// TestRegistryPacksCachedMissIsServedWithoutARequest.
func TestRegistryPacksCachedMissIsServedWithoutARequest(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	now := time.Now()
	cache := newMemCache()
	cache.seed(cacheSourceRegistryPrefix+"GhostClass", []byte("null"), now.Add(-time.Hour))

	r := resolverWith(srv, cache, now)
	packs, unresolved, errs := r.RegistryPacks(context.Background(), []string{"GhostClass"})
	if hits != 0 {
		t.Errorf("a cached miss issued %d request(s)", hits)
	}
	if len(packs) != 0 || len(errs) != 0 {
		t.Errorf("packs=%+v errs=%v", packs, errs)
	}
	if len(unresolved) != 1 || unresolved[0] != "GhostClass" {
		t.Errorf("unresolved = %v", unresolved)
	}
}

// TestRegistryPacksEmptyInput issues nothing.
func TestRegistryPacksEmptyInput(t *testing.T) {
	var hits int64
	srv := newTLSRegistryHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
	}))
	r := resolverWith(srv, newMemCache(), time.Now())
	packs, unresolved, errs := r.RegistryPacks(context.Background(), nil)
	if len(packs) != 0 || len(unresolved) != 0 || len(errs) != 0 || hits != 0 {
		t.Errorf("packs=%v unresolved=%v errs=%v hits=%d", packs, unresolved, errs, hits)
	}
}

// TestDecodeRegistryPackHandlesACachedMiss: the JSON null a miss is stored as
// must decode to ErrRegistryNotFound, not to a bogus empty pack.
func TestDecodeRegistryPackHandlesACachedMiss(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"cached miss", `null`},
		{"the real 404 body", `{"error":"","message":"No node found containing the specified ComfyUI node name"}`},
		{"empty object", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRegistryPack([]byte(tc.raw), "X"); !errors.Is(err, ErrRegistryNotFound) {
				t.Errorf("err = %v, want ErrRegistryNotFound", err)
			}
		})
	}
	if _, err := DecodeRegistryPack([]byte(`<html>`), "X"); err == nil {
		t.Error("an undecodable body must error")
	}
}
