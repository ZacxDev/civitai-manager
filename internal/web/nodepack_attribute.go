package web

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// Custom-node ATTRIBUTION: turning a preflight's flat list of missing
// class_types into "which node pack provides each of these, and can we install
// it". See claudedocs/CUSTOM-NODE-RESOLUTION-DESIGN.md.
//
// Two properties shape everything here:
//
//   - Attribution is PARTIAL in reality (4 of 7 classes unresolved on the
//     design's real target workflow), so "we could not place this class" is a
//     first-class result the UI renders deliberately, never an error.
//   - ComfyUI-Manager is OPTIONAL. Its absence removes the install button and
//     nothing else: attribution and the manual command still render.
//
// Like resolveMissingModels, this runs ONCE at run settle — never on the ~1s
// status poll — because it can reach the network.

// managerClient is the ComfyUI-Manager surface the attribution and install
// flows need. It is an interface so tests can inject a fake; *comfy.Client
// satisfies it. It is deliberately SEPARATE from comfyClient: Manager is an
// optional third-party add-on, and a fake ComfyUI for the run tests must not be
// forced to grow Manager methods it never exercises.
type managerClient interface {
	// ManagerProbe reports whether a Manager web API is actually answering.
	// Present==false is a normal outcome, not an error.
	ManagerProbe(ctx context.Context) (*comfy.ManagerInfo, error)
	// ManagerMappings / ManagerNodePacks are the two attribution index documents.
	// getlist (ManagerNodePacks) is GONE in Manager V4's default mode.
	ManagerMappings(ctx context.Context) (json.RawMessage, error)
	ManagerNodePacks(ctx context.Context) (json.RawMessage, error)
	// ManagerInstall delegates the install to Manager. We never write custom_nodes/.
	ManagerInstall(ctx context.Context, info *comfy.ManagerInfo, pack comfy.Pack) error
	// ManagerQueueStatus is Manager's task-queue counters (progress only —
	// IsProcessing is false BEFORE a queue starts, so it can never mean "done").
	ManagerQueueStatus(ctx context.Context, info *comfy.ManagerInfo) (*comfy.ManagerQueueStatus, error)
	// ManagerInstalledDiff is the portable "it landed, restart pending" signal.
	ManagerInstalledDiff(ctx context.Context) ([]string, error)
	// ManagerReboot restarts ComfyUI, refusing (ErrQueueBusy) while its queue is busy.
	ManagerReboot(ctx context.Context, info *comfy.ManagerInfo) error
}

// nodeAttributeBudget bounds the WHOLE at-settle attribution pass (Manager
// indexes + the static index + N Registry lookups) so an unreachable third-party
// host degrades to "unattributed" instead of stalling the terminal render.
const nodeAttributeBudget = 25 * time.Second

// maxRegistryClasses caps how many still-unplaced classes are looked up against
// api.comfy.org in one pass. The Registry has NO batch endpoint, so this is one
// request per class; a pathological graph must not turn one failed run into
// hundreds of outbound requests. The overflow is reported as unattributed.
const maxRegistryClasses = 40

// nodeAttribution is the at-settle attribution result carried on the run job.
// It is read-only after settle.
type nodeAttribution struct {
	// Packs are the attributed packs, strongest confidence rung first.
	Packs []comfy.Pack
	// Unattributed are the missing classes no rung could place. Common — render it.
	Unattributed []string
	// ManagerPresent records whether a ComfyUI-Manager web API actually answered.
	// False means NO install affordance may be offered at all.
	ManagerPresent bool
	// ManagerVersion / ManagerNote are Manager's self-reported version and a
	// human explanation of an absent/degraded state. Both are rendered escaped.
	ManagerVersion string
	ManagerNote    string
	// ComfyRoot is the configured ComfyUI install root, captured at settle so the
	// panel can render a real manual-install command instead of a placeholder
	// path. It travels here rather than as yet another render parameter threaded
	// through the whole run-status fragment chain; it is set OUTSIDE the
	// attribution seam (from config), never derived from a third-party index.
	ComfyRoot string
	// RemoteLookup records whether the ONLINE rungs (Comfy Registry + the static
	// extension-node-map) were allowed to run for this pass — i.e. the effective
	// value of config `resolve_node_packs`. The panel's data-egress disclosure reads
	// it so it can never claim egress that did not and will not happen. Like
	// ComfyRoot it is set OUTSIDE the attribution seam, from config, so it stays
	// correct even when a test injects a canned attribution.
	RemoteLookup bool
}

// managerClient returns the ComfyUI-Manager client: the test seam when set,
// otherwise a live client built from comfy_url. Nil when no ComfyUI is
// configured (attribution then degrades to the static index + Registry).
func (s *Server) managerClient() managerClient {
	if s.managerClientFn != nil {
		return s.managerClientFn()
	}
	if strings.TrimSpace(s.cfg.ComfyURL) == "" {
		return nil
	}
	return comfy.NewClient(s.cfg.ComfyURL, s.cfg.ComfyToken)
}

// nodePackResolver returns the cache-aware EGRESS resolver (static
// extension-node-map + Comfy Registry) over the SQLite node-pack cache, or nil
// when the online lookups are disabled (config `resolve_node_packs: false`).
//
// This mirrors hfClientOrNil: the opt-out lives at the point the outbound client
// is CONSTRUCTED, so a disabled lookup never builds an http.Client, never
// resolves a hostname and never opens a socket — as opposed to building the
// resolver and then declining to use it. The resolver itself stays a pure
// capability with no policy in it.
//
// Only the two PUBLIC hosts are gated. ComfyUI-Manager (managerClient) is on
// loopback and is deliberately unaffected: with the flag off, attribution still
// works fully whenever Manager is installed.
func (s *Server) nodePackResolver() *comfy.NodePackResolver {
	if s.nodePackResolverFn != nil {
		return s.nodePackResolverFn()
	}
	if !s.cfg.ResolveNodePacks {
		return nil
	}
	return comfy.NewNodePackResolver(s.store)
}

// attributeMissingNodes maps missing class_types to the packs that provide them.
//
// The rungs are MERGED, not ranked — they are complementary (measured: static
// map alone 3/7, Registry alone 4/7, union 6/7, plus the pattern rung for the
// 7th) — and each pack records which rung produced it so the UI can label
// confidence. Only classes a previous rung could not place are passed to the
// next one, so a fully-resolving Manager costs ZERO outbound requests.
//
// It must NOT be called from a render/poll path: it reaches ComfyUI-Manager on
// loopback and (for anything Manager could not place) api.comfy.org and
// raw.githubusercontent.com — the latter two only when `resolve_node_packs` is
// on.
func (s *Server) attributeMissingNodes(parent context.Context, classes []string) nodeAttribution {
	if s.attributeFn != nil {
		return s.attributeFn(parent, classes)
	}
	return s.realAttributeMissingNodes(parent, classes)
}

// realAttributeMissingNodes is the production attribution seam.
func (s *Server) realAttributeMissingNodes(parent context.Context, classes []string) nodeAttribution {
	out := nodeAttribution{}
	if len(classes) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(parent, nodeAttributeBudget)
	defer cancel()

	remaining := classes
	var sets [][]comfy.Pack

	// Rung 1+3: ComfyUI-Manager's own indexes (exact class match, then the
	// nodename_pattern regexes). No egress at all — Manager is on loopback.
	if mc := s.managerClient(); mc != nil {
		info, err := mc.ManagerProbe(ctx)
		if err != nil {
			s.log.Warn("probe ComfyUI-Manager failed", "err", err)
		}
		if info != nil {
			out.ManagerPresent = info.Present
			out.ManagerVersion = info.Version
			out.ManagerNote = info.Note
		}
		if info != nil && info.Present {
			mappings, mErr := mc.ManagerMappings(ctx)
			if mErr != nil {
				s.log.Warn("fetch ComfyUI-Manager mappings failed", "err", mErr)
			} else {
				// getlist is the ONLY source of cnr_latest, hence of Installable. Its
				// absence (Manager V4 default mode) is not fatal — every pack is then
				// simply not auto-installable.
				var getlist json.RawMessage
				if info.HasNodePackList {
					if gl, gErr := mc.ManagerNodePacks(ctx); gErr == nil {
						getlist = gl
					} else {
						s.log.Warn("fetch ComfyUI-Manager node-pack list failed", "err", gErr)
					}
				}
				// BuildIndex always returns a usable index, even on malformed input.
				ix, bErr := comfy.BuildIndex(mappings, getlist)
				if bErr != nil {
					s.log.Warn("build node-pack index failed", "err", bErr)
				}
				packs, un := ix.Attribute(remaining)
				sets, remaining = append(sets, packs), un
			}
		}
	}

	// Both remaining rungs are OUTBOUND, so they live behind ONE gate: the resolver
	// is built only when something is still unplaced (a fully-resolving Manager
	// costs zero requests) AND `resolve_node_packs` is on. A nil res IS the opt-out
	// — no resolver, hence no HTTP client, hence no DNS lookup and no connection to
	// either public host; whatever Manager could not place stays unattributed.
	if len(remaining) > 0 {
		if res := s.nodePackResolver(); res != nil {
			packs, un := s.attributeRemote(ctx, res, remaining)
			sets, remaining = append(sets, packs...), un
		}
	}

	out.Packs = comfy.MergePacks(sets...)
	out.Unattributed = remaining
	return out
}

// attributeRemote runs the two OUTBOUND rungs (static extension-node-map, then
// the Comfy Registry) over the classes no local rung could place. It is only ever
// reached with a non-nil resolver, i.e. with `resolve_node_packs` on.
func (s *Server) attributeRemote(ctx context.Context, res *comfy.NodePackResolver, remaining []string) ([][]comfy.Pack, []string) {
	var sets [][]comfy.Pack

	// Rung 1' : the static extension-node-map, the no-Manager source. Same
	// document shape as getmappings but WITHOUT the nodename_pattern rules and
	// without cnr_latest, so nothing it produces is auto-installable.
	if len(remaining) > 0 {
		if raw, err := res.StaticIndex(ctx); err != nil {
			s.log.Warn("fetch static node-pack index failed", "err", err)
		} else {
			ix, bErr := comfy.BuildIndex(raw, nil)
			if bErr != nil {
				s.log.Warn("build static node-pack index failed", "err", bErr)
			}
			packs, un := ix.Attribute(remaining)
			sets, remaining = append(sets, packs), un
		}
	}

	// Rung 2: the Comfy Registry, one request per still-unplaced class (no batch
	// endpoint exists), capped and cached.
	if len(remaining) > 0 {
		ask, overflow := remaining, []string(nil)
		if len(ask) > maxRegistryClasses {
			ask, overflow = ask[:maxRegistryClasses], ask[maxRegistryClasses:]
		}
		packs, un, errs := res.RegistryPacks(ctx, ask)
		for _, e := range errs {
			s.log.Warn("comfy registry lookup failed", "err", e)
		}
		sets = append(sets, packs)
		remaining = append(un, overflow...)
	}

	return sets, remaining
}
