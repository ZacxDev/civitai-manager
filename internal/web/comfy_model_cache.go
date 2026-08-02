package web

import (
	"encoding/json"
	"sync"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE comfy_model_cache (migration 0019) — WHO WRITES IT, WHO READS IT.
// ─────────────────────────────────────────────────────────────────────────────
// A resource chip has three states: present in the local library (✓), absent
// from the library but resolvable by ComfyUI (◎), or neither (✗). The middle
// state needs to know what ComfyUI has installed — and a page render must never
// make an outbound request to find out, so the answer is served from a cache.
//
// 🔴 A CACHE WITH NO WRITER IS AN INERT FEATURE. The first version of this work
// shipped Get/Put/Invalidate with ZERO non-test callers: Get always returned nil,
// so the ◎ state could never render on any page, while the migration's header
// documented three population triggers that did not exist. The two functions
// below are the writers, and they are wired into the only two places in the app
// that already hold a fresh /object_info in hand.
//
//	cacheComfyObjectInfo   ← realRun (local run) and the cloud run's local probe
//	invalidateComfyModelCache ← a finished library scan
//
// ⚠ HONEST SCOPE, stated because 0019's header still names a third trigger:
// there is NO manual "Refresh" control, and this rework did not add one (a new
// endpoint is its own CSRF/auth surface and was out of scope). The cache is
// therefore as fresh as the user's last ComfyUI run, and empty until their first
// one. That is a fail-CLOSED default: with no cache, every chip falls back to
// ✓/✗ exactly as it did before this feature existed.

// cacheComfyObjectInfo stores a freshly fetched /object_info body.
//
// BEST-EFFORT BY DESIGN: this is called on the run path, and a workflow run must
// never fail because a display cache could not be written. A failure is logged
// and swallowed — the only consequence is that chips keep showing ✗ instead of ◎.
func (s *Server) cacheComfyObjectInfo(raw []byte) {
	if len(raw) == 0 {
		return
	}
	if err := s.store.PutComfyObjectInfo(raw); err != nil {
		s.log.Warn("cache comfy object_info", "err", err)
	}
}

// invalidateComfyModelCache drops the cached /object_info.
//
// Called when a library scan finishes. The reasoning, since it is not obvious:
// the two file sets are COUPLED in practice — `comfy_model_path` normally points
// inside a ComfyUI install, so a scan is the app's best available signal that the
// user has been adding or removing model files, and ComfyUI re-enumerates its own
// directories on every /object_info fetch. Dropping the row makes the app forget
// rather than assert a stale "ComfyUI has this"; the next run repopulates it.
//
// Also best-effort: a scan must not fail over a cache row.
func (s *Server) invalidateComfyModelCache() {
	if err := s.store.InvalidateComfyModelCache(); err != nil {
		s.log.Warn("invalidate comfy model cache", "err", err)
	}
}

// comfyNodeOrigins builds the built-in-vs-custom index for the cloud panel's
// custom-node detection, from the SAME cached /object_info row as the model index
// above. Nothing new is stored: the 0019 blob already carries `python_module` for
// every node type, so reusing it costs one query and no schema change — and a
// second cache of the same 4.66 MB document could go stale independently of this
// one, which is the failure this avoids.
//
// Returns nil on an empty/corrupt cache; ResolveResources then falls back to
// coreNodeClasses, which is the pre-existing behaviour.
//
// ⚠ HONEST FRESHNESS LIMIT, inherited from 0019 and not introduced here: the row
// is written on a ComfyUI run and dropped on a library scan, so INSTALLING a
// custom node does not invalidate it. Until the user's next run, a newly added
// pack's node types are absent from the cached payload and read as Unknown — the
// fallback tier, i.e. exactly today's answer. It degrades to the old behaviour,
// never to a confident wrong one.
// 🔴 EVERY FAILURE HERE IS LOGGED, because every one of them silently reinstates
// the 62%-false-positive banner this classifier exists to fix. The sibling memo
// twenty lines below (newComfyModelIndex) already logs the identical decode
// failure; a silent twin is how "the banner is wrong again" becomes undiagnosable
// from the outside. A missing row is NOT logged — a cold cache is the normal
// fresh-install state, not a fault.
//
// ⚠ THE BUILTIN/CUSTOM SPLIT IS NOT LOGGED HERE — see logNodeOriginSplit below.
// It used to be, and that put it on the one path the dominant traffic never takes:
// this function runs only as resolveResources' FALLBACK, so on a UI-format workflow
// with a reachable ComfyUI (all 71 workflows on the operator's real DB) it is never
// called and the split was never recorded at all.
func (s *Server) comfyNodeOrigins() map[string]comfy.NodeOrigin {
	ent, err := s.store.GetComfyObjectInfo()
	if err != nil {
		s.log.Warn("read cached comfy object_info", "err", err)
		return nil
	}
	if ent == nil {
		return nil // cold cache: expected, not a fault
	}
	idx := comfy.NodeOrigins(ent.ObjectInfoJSON)
	if len(idx) == 0 {
		// NodeOrigins reports "could not classify anything" as a nil map rather than
		// an error, so there is no err to attach — the byte count is what separates a
		// truncated/corrupt row from a well-formed but empty one.
		s.log.Warn("decode cached comfy object_info", "bytes", len(ent.ObjectInfoJSON))
		return nil
	}
	return idx
}

// logNodeOriginSplit records the builtin/custom split of whichever tier answered.
// tier is "fresh" (the /object_info the handler just fetched) or "cache" (the 0019
// row) — the caller knows, and nothing downstream can work it out afterwards.
//
// ⚠ THE FAILURE THIS MAKES DIAGNOSABLE: if no installed pack registers under
// `custom_nodes.*` (a repackaged install, a vendored fork), every class reads
// Builtin, the banner vanishes entirely, and neither the code nor any test can tell
// — the page just quietly stops warning. A 0-custom split is the signal.
//
// 🔴 IT IS Debug, AND IT IS DELIBERATELY NOT A Warn. ⚠ The comment this replaces
// called it "the only externally visible signal", which overstated twice over: the
// server logger is LevelInfo unless the operator runs `serve --verbose`
// (cli/root.go), so this line is invisible by default. Promoting the 0-custom case
// to Warn was considered and REJECTED — an install with no custom-node packs at all
// is completely ordinary, and 0 custom is then the CORRECT answer, so a Warn would
// fire on every cloud-panel render of a perfectly healthy install. A signal that
// cries wolf on the common case is worse than one you have to opt into. The genuine
// faults (unreadable row, undecodable body — either tier) are Warn and do reach the
// default level.
//
// Empty is silent: it means a cold cache (the normal fresh-install state) or a
// failure the caller has already logged with its own byte count.
func (s *Server) logNodeOriginSplit(tier string, idx map[string]comfy.NodeOrigin) {
	if len(idx) == 0 {
		return
	}
	builtin, custom := 0, 0
	for _, o := range idx {
		switch o {
		case comfy.NodeOriginBuiltin:
			builtin++
		case comfy.NodeOriginCustom:
			custom++
		}
	}
	s.log.Debug("classified comfy object_info",
		"tier", tier, "types", len(idx), "builtin", builtin, "custom", custom)
}

// comfyModelIndex is the PER-REQUEST memo behind the resolver's comfyResource.
//
// 🔴 WHY THIS EXISTS AT ALL — the shape it replaces was O(chips) full decodes.
// The first version re-read the blob from SQLite and json.Unmarshal'd the ENTIRE
// ObjectInfo once PER CHIP. Measured against the real live payload (4,661,987
// bytes) that is ~88 ms per call; the operator's real database holds 305 resource
// entries across 52 workflows, i.e. ~27 s of render time plus tens of GB of
// transient allocation to draw ONE page. Same class as the documented
// "store.ListQueue on a render path" prohibition.
//
// This memo decodes at most ONCE per resolver — and a resolver is built once per
// request — after which every chip is a map lookup. It is LAZY: a page whose
// resources are all present locally never touches the row at all.
//
// The STORED FORMAT IS DELIBERATELY UNCHANGED. 0019 keeps the full blob and
// derives presence at query time so the cache stays correct when new loader node
// types appear; baking a basename set in at insert time would trade that away.
// The decode cost is paid once per request instead of being designed out of the
// schema.
//
// sync.Once rather than a bare nil check: workflowResolver is copied by value to
// several renderers, and while rendering is single-goroutine today, the closures
// all share this one struct — Once makes the memo safe if that ever stops being
// true, at no measurable cost.
type comfyModelIndex struct {
	once sync.Once
	load func() map[string]struct{}
	set  map[string]struct{}
}

// has reports whether ComfyUI can resolve a file with this basename, decoding the
// cached payload on first use.
func (c *comfyModelIndex) has(basename string) bool {
	if c == nil {
		return false
	}
	c.once.Do(func() {
		if c.load != nil {
			c.set = c.load()
		}
	})
	return comfy.HasModelFile(c.set, basename)
}

// newComfyModelIndex builds the memo over the store's cached /object_info. The
// read and the decode BOTH happen inside the memo, so an empty cache costs one
// query per request at most, and a populated one costs one query plus one decode.
func (s *Server) newComfyModelIndex() *comfyModelIndex {
	return &comfyModelIndex{load: func() map[string]struct{} {
		ent, err := s.store.GetComfyObjectInfo()
		if err != nil || ent == nil || len(ent.ObjectInfoJSON) == 0 {
			return nil
		}
		var oi comfy.ObjectInfo
		if err := json.Unmarshal(ent.ObjectInfoJSON, &oi); err != nil {
			// A corrupt row must not break a page: fall back to "ComfyUI has
			// nothing", which renders the pre-feature ✓/✗ chips.
			s.log.Warn("decode cached comfy object_info", "err", err)
			return nil
		}
		return comfy.ModelFileChoices(oi)
	}}
}
