package web

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// ── THE DECODED NODE SCHEMA, MEMOISED ────────────────────────────────────────
//
// 🔴 WHY THIS EXISTS. The readiness fragment is a SEPARATE lazy request per
// workflow-page view, and it decoded the whole 0019 payload every single time.
// MEASURED against the operator's real cached row — 4,661,986 bytes, 2,462 node
// types, 5 iterations: 73 ms, 79 ms, 79 ms, 82 ms, 113 ms per decode, against a
// ~0.77 ms page render. That is the same class of cost comfy_model_cache.go's
// comfyModelIndex was built to stop (its header records ~88 ms per decode on the
// same payload), except that memo is PER-REQUEST and so buys this path nothing —
// one readiness request performs exactly one decode.
//
// So the memo has to live on the SERVER, keyed on the row's content.
//
// 🔴 THE KEY IS THE RAW BLOB, COMPARED WITH bytes.Equal — not updated_at.
// PutComfyObjectInfo stamps nowRFC3339(), i.e. SECOND granularity, so
// (updated_at, len) can collide silently and a stale schema would then answer for
// a ComfyUI that has changed. bytes.Equal is exact, and on a 4.66 MB blob it is a
// memcmp, not a parse.
//
// HONEST RESIDUE, stated so nobody re-measures it as a win it is not: the SQLite
// READ still happens on every request — GetComfyObjectInfo pulls the whole blob,
// measured at ~18 ms on that row. Only the decode is avoided. Removing the read as
// well would need a store method that fetches updated_at alone, and that trades the
// exact key above for a second-granularity one.
//
// MEMORY: the memo retains one copy of the blob plus its decoded form. That is
// deliberate — the decoded ObjectInfo is much larger than the 4.66 MB source, so
// keeping the source to key on it is a rounding error against what the memo already
// holds.
//
// SAFETY: the cached comfy.ObjectInfo is handed out to concurrent readers and is
// treated as READ-ONLY by everything that takes one (verified: no assignment to or
// delete from an ObjectInfo map anywhere in internal/comfy). Do not start mutating
// it downstream without giving each caller its own copy.
type readinessSchemaMemo struct {
	mu   sync.Mutex
	raw  []byte
	info comfy.ObjectInfo
	// decodes counts how many times the payload was actually decoded. It exists so a
	// test can prove the memo is CONSULTED rather than timing it — a timing assertion
	// on an 80 ms decode is exactly the kind of guard that goes flaky under -race and
	// then gets deleted. Read/written atomically, outside mu.
	decodes atomic.Int64
}

// get returns the decoded schema for this exact payload, or nil on a miss.
func (m *readinessSchemaMemo) get(raw []byte) comfy.ObjectInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.info == nil || !bytes.Equal(m.raw, raw) {
		return nil
	}
	return m.info
}

// put installs a decoded schema under its payload. The caller keeps its own copy of
// raw; nothing here mutates it.
func (m *readinessSchemaMemo) put(raw []byte, info comfy.ObjectInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw, m.info = raw, info
}

// readinessSchema resolves the node schema the readiness line checks against,
// returning the reason a caller must report when it cannot.
//
// A reason of "" means the schema is usable. The three failure reasons are
// unchanged from the inline version this replaces, including the load-bearing
// zero-node-types case: a well-formed but EMPTY payload is a row we cannot trust,
// not "nothing is installed", and treating it as data would make every class in
// every graph look missing.
//
// 🔴 A FAILED DECODE IS NEVER MEMOISED. The memo is a cache of a good answer; a
// corrupt row costs one (fast-failing) decode attempt per request and, more
// importantly, can never park an unusable value where a later good write would be
// shadowed by it.
func (s *Server) readinessSchema() (comfy.ObjectInfo, readinessReason) {
	ent, err := s.store.GetComfyObjectInfo()
	if err != nil {
		s.log.Warn("readiness: read cached comfy object_info", "err", err)
		return nil, reasonUnreadableSchema
	}
	if ent == nil || len(ent.ObjectInfoJSON) == 0 {
		return nil, reasonColdCache // cold cache: expected, not a fault
	}
	if info := s.schemaMemo.get(ent.ObjectInfoJSON); info != nil {
		return info, ""
	}

	var info comfy.ObjectInfo
	s.schemaMemo.decodes.Add(1)
	if uerr := json.Unmarshal(ent.ObjectInfoJSON, &info); uerr != nil {
		s.log.Warn("readiness: decode cached comfy object_info",
			"err", uerr, "bytes", len(ent.ObjectInfoJSON))
		return nil, reasonUnreadableSchema
	}
	if len(info) == 0 {
		s.log.Warn("readiness: cached comfy object_info decoded to zero node types",
			"bytes", len(ent.ObjectInfoJSON))
		return nil, reasonUnreadableSchema
	}
	s.schemaMemo.put(ent.ObjectInfoJSON, info)
	return info, ""
}
