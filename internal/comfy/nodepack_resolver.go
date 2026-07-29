package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Cache-aware attribution: the piece that keeps this feature from hammering two
// third-party hosts every time a workflow page renders.
//
// The freshness policy is the SAME fail-open contract as the community-image
// cache: serve fresh from cache -> else fetch -> else fall back to the STALE
// entry when the fetch fails. Only SUCCESSFUL responses are cached, so the
// fail-open path can never serve a poisoned empty/error body as though it were a
// real index.

// Default cache lifetimes. The static index changes slowly (it is a curated
// third-party file), and a class-to-pack mapping is close to immutable, so both
// TTLs are long: this cache exists to stop N-requests-per-render, not to track
// upstream closely.
const (
	// DefaultStaticIndexTTL is how long the extension-node-map snapshot is served
	// without refetching.
	DefaultStaticIndexTTL = 24 * time.Hour
	// DefaultRegistryTTL is how long one Registry class answer is served without
	// refetching. Misses are cached for the same period — an unattributable class
	// is a stable fact, not a transient failure.
	DefaultRegistryTTL = 7 * 24 * time.Hour
)

// NodePackCache is the persistence the resolver needs. *store.Store satisfies it
// (see its GetNodePackRaw/PutNodePackRaw). It is an interface here so that
// internal/comfy does not depend on internal/store — the dependency runs the
// other way for every other package, and the resolver stays unit-testable
// without a database.
type NodePackCache interface {
	// GetNodePackRaw returns the cached body for source. A missing entry is
	// (nil, zero time, nil) — NOT an error.
	GetNodePackRaw(source string) ([]byte, time.Time, error)
	// PutNodePackRaw stores a SUCCESSFUL response body for source.
	PutNodePackRaw(source string, raw []byte) error
}

// Cache source keys, mirrored from the store's migration comment so both sides
// agree on the key space without a package dependency.
const (
	cacheSourceExtensionNodeMap = "extension-node-map"
	cacheSourceRegistryPrefix   = "registry:"
)

// NodePackResolver resolves missing classes to packs, caching both egress
// sources. Cache may be nil (every call then goes straight to the network),
// which is the correct behaviour for a caller with no database.
type NodePackResolver struct {
	Registry *RegistryClient
	Cache    NodePackCache

	// StaticIndexTTL / RegistryTTL default to the constants above when zero.
	StaticIndexTTL time.Duration
	RegistryTTL    time.Duration

	// now is a test seam.
	now func() time.Time
}

// NewNodePackResolver builds a resolver over the hardened registry client.
// cache may be nil.
func NewNodePackResolver(cache NodePackCache) *NodePackResolver {
	return &NodePackResolver{
		Registry:       NewRegistryClient(),
		Cache:          cache,
		StaticIndexTTL: DefaultStaticIndexTTL,
		RegistryTTL:    DefaultRegistryTTL,
	}
}

func (r *NodePackResolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *NodePackResolver) staticTTL() time.Duration {
	if r.StaticIndexTTL > 0 {
		return r.StaticIndexTTL
	}
	return DefaultStaticIndexTTL
}

func (r *NodePackResolver) registryTTL() time.Duration {
	if r.RegistryTTL > 0 {
		return r.RegistryTTL
	}
	return DefaultRegistryTTL
}

// StaticIndex returns the extension-node-map index body, cached.
//
// Fresh cache -> serve it. Stale/absent -> fetch and re-cache. Fetch failed but a
// STALE entry exists -> serve the stale entry (fail-open: an out-of-date
// attribution is far better than none). Nothing cached and the fetch failed ->
// the fetch error.
func (r *NodePackResolver) StaticIndex(ctx context.Context) (json.RawMessage, error) {
	cached, fetchedAt := r.get(cacheSourceExtensionNodeMap)
	if len(cached) > 0 && r.clock().Sub(fetchedAt) < r.staticTTL() {
		return json.RawMessage(cached), nil
	}
	fresh, err := r.Registry.FetchExtensionNodeMap(ctx)
	if err != nil {
		if len(cached) > 0 {
			return json.RawMessage(cached), nil // fail open on the stale copy
		}
		return nil, err
	}
	r.put(cacheSourceExtensionNodeMap, fresh)
	return fresh, nil
}

// RegistryPacks resolves classes through the Comfy Registry, cached per class.
//
// Each class independently follows the fresh -> fetch -> stale ladder, so one
// class's network failure never costs the others their answer. A cached MISS is
// a real answer (stored as JSON null) and is served without a request. Errors are
// returned for reporting but are never fatal: partial attribution beats none.
func (r *NodePackResolver) RegistryPacks(ctx context.Context, classes []string) (packs []Pack, unresolved []string, errs []error) {
	classes = sortedUnique(classes)
	if len(classes) == 0 {
		return nil, nil, nil
	}

	results := make([]registryOutcome, len(classes))

	// Cache reads/writes happen on this goroutine's side of a mutex because the
	// cache implementation (a *sql.DB-backed store) is shared.
	var cacheMu sync.Mutex

	sem := make(chan struct{}, registryConcurrency)
	var wg sync.WaitGroup
	for i, cls := range classes {
		// Serve a fresh cache entry without occupying a request slot at all.
		cacheMu.Lock()
		cached, fetchedAt := r.get(cacheSourceRegistryPrefix + cls)
		cacheMu.Unlock()
		if len(cached) > 0 && r.clock().Sub(fetchedAt) < r.registryTTL() {
			results[i] = decodeCachedRegistry(cached, cls)
			continue
		}

		wg.Add(1)
		go func(i int, cls string, stale []byte) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = registryOutcome{err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			raw, err := r.Registry.LookupClassRaw(ctx, cls)
			switch {
			case err == nil:
				cacheMu.Lock()
				r.put(cacheSourceRegistryPrefix+cls, raw)
				cacheMu.Unlock()
				results[i] = decodeCachedRegistry(raw, cls)
			case errors.Is(err, ErrRegistryNotFound):
				// A miss is a stable fact worth caching, so the next render does
				// not re-ask for a class the Registry has never heard of.
				cacheMu.Lock()
				r.put(cacheSourceRegistryPrefix+cls, []byte("null"))
				cacheMu.Unlock()
				results[i] = registryOutcome{}
			default:
				if len(stale) > 0 {
					results[i] = decodeCachedRegistry(stale, cls) // fail open
					return
				}
				results[i] = registryOutcome{err: err}
			}
		}(i, cls, cached)
	}
	wg.Wait()

	for i, res := range results {
		switch {
		case res.ok:
			packs = append(packs, res.pack)
		case res.err != nil:
			errs = append(errs, res.err)
			unresolved = append(unresolved, classes[i])
		default:
			unresolved = append(unresolved, classes[i])
		}
	}
	return MergePacks(packs), unresolved, errs
}

// registryOutcome is one class's resolution result inside RegistryPacks.
type registryOutcome struct {
	pack Pack
	ok   bool
	err  error
}

// decodeCachedRegistry turns a cached body into an outcome. A cached miss (the
// JSON null) and an undecodable body both read as "unattributed", never as an
// error the user has to see — the body came from our own cache.
func decodeCachedRegistry(raw []byte, class string) registryOutcome {
	p, err := DecodeRegistryPack(raw, class)
	if err != nil {
		return registryOutcome{}
	}
	return registryOutcome{pack: p, ok: true}
}

// get reads the cache, treating any cache error as a miss: a broken cache must
// degrade to a network fetch, never fail the attribution.
func (r *NodePackResolver) get(source string) ([]byte, time.Time) {
	if r.Cache == nil {
		return nil, time.Time{}
	}
	raw, fetchedAt, err := r.Cache.GetNodePackRaw(source)
	if err != nil {
		return nil, time.Time{}
	}
	return raw, fetchedAt
}

// put writes the cache, ignoring a write failure: caching is an optimisation, so
// a read-only or full database must not break attribution.
func (r *NodePackResolver) put(source string, raw []byte) {
	if r.Cache == nil || len(raw) == 0 {
		return
	}
	_ = r.Cache.PutNodePackRaw(source, raw)
}
