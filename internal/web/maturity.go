package web

import "strings"

// ---------------------------------------------------------------------------
// The maturity scale.
//
// CivitAI rates a piece of content on a five-step scale that its API exposes as
// a NUMBER — the `browsingLevel` bit value. Live-probed 2026-07-31 against
// /api/v1/images?modelVersionId=3112728&limit=100 (one request per ceiling):
//
//	nsfwLevel "None"   -> browsingLevel 1    (PG)
//	nsfwLevel "Soft"   -> browsingLevel 2    (PG-13)
//	nsfwLevel "Mature" -> browsingLevel 4    (R)
//	nsfwLevel "X"      -> browsingLevel 8    (X)
//	nsfwLevel "X"      -> browsingLevel 16   (XXX)   <- THE SAME STRING
//
// 🔴 THE STRING LABEL COLLAPSES X AND XXX. On `nsfw=X&limit=100` the response
// carried 41 items at browsingLevel 8 and 40 at browsingLevel 16 — every one of
// the 81 labelled `"X"`. A scale built on the string therefore CANNOT address
// the top of the range: "X only" and "XXX only" are indistinguishable, and a
// range ending at X would leak XXX. Everything below reads the NUMBER.
//
// (32 = "Blocked" exists in the same bit space but is a moderation bucket, not a
// user-selectable step. It is deliberately absent from the scale and therefore
// falls into maturityUnknown — fail-closed, never rendered.)
// ---------------------------------------------------------------------------

// maturityLevel is one step on the scale, valued as CivitAI's numeric
// browsingLevel so ordering is plain integer ordering (1 < 2 < 4 < 8 < 16).
type maturityLevel int

const (
	maturityPG   maturityLevel = 1
	maturityPG13 maturityLevel = 2
	maturityR    maturityLevel = 4
	maturityX    maturityLevel = 8
	maturityXXX  maturityLevel = 16
)

// maturityUnknown is the sentinel for content whose level is ABSENT, garbage, or
// a value outside the five-step scale (notably 32 = Blocked). It sits ABOVE every
// real level, so no range can ever contain it and such content is always OMITTED.
// That is the deliberate fail-closed posture: an unrated image is never rendered
// on the assumption that it is tame.
//
// It is NOT what "this surface carries no level at all" means — see
// maturityRange.contains and the outputs rail/gallery, which never consult a
// range in the first place.
const maturityUnknown maturityLevel = 1 << 20

// maturityScale is the ordered, user-selectable scale, lowest first. It is the
// single source of truth for the nav control's options and for range validation.
var maturityScale = []maturityLevel{maturityPG, maturityPG13, maturityR, maturityX, maturityXXX}

// slug is the stable, lowercase wire/storage token for a level. Persisted in the
// settings row and submitted by the nav control, so it must not drift.
func (l maturityLevel) slug() string {
	switch l {
	case maturityPG:
		return "pg"
	case maturityPG13:
		return "pg13"
	case maturityR:
		return "r"
	case maturityX:
		return "x"
	case maturityXXX:
		return "xxx"
	default:
		return ""
	}
}

// label is the human-facing name shown in the control.
func (l maturityLevel) label() string {
	switch l {
	case maturityPG:
		return "PG"
	case maturityPG13:
		return "PG-13"
	case maturityR:
		return "R"
	case maturityX:
		return "X"
	case maturityXXX:
		return "XXX"
	default:
		return "?"
	}
}

// maturityFromSlug resolves a stored/submitted token. Unknown → (0, false); the
// caller decides the fallback rather than silently getting a level.
func maturityFromSlug(s string) (maturityLevel, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, l := range maturityScale {
		if l.slug() == s {
			return l, true
		}
	}
	return 0, false
}

// maturityFromBrowsingLevel maps CivitAI's NUMERIC level onto the scale. Only the
// five exact scale values are recognised; anything else — 0, 32 (Blocked), a
// future value, a garbage decode — becomes maturityUnknown and is omitted.
//
// 🔴 There is deliberately NO maturityFromNSFWLabel counterpart taking the string
// `nsfwLevel`. See the header: "X" means EITHER 8 or 16, so a string-driven
// mapping cannot distinguish X from XXX and would make the top of the range a
// lie. Callers must reach for the number.
func maturityFromBrowsingLevel(n int) maturityLevel {
	switch maturityLevel(n) {
	case maturityPG, maturityPG13, maturityR, maturityX, maturityXXX:
		return maturityLevel(n)
	default:
		return maturityUnknown
	}
}

// ---------------------------------------------------------------------------
// The range.
// ---------------------------------------------------------------------------

// maturityRange is the app-wide, inclusive PG..XXX band the user has selected. It
// REPLACED the old two-state NSFW blur/show toggle outright: content outside the
// band is OMITTED SERVER-SIDE (its URL never reaches the DOM), content inside
// renders PLAIN. There is no blur any more — blur was a browser-side CSS filter
// that still shipped the bytes.
type maturityRange struct {
	Min maturityLevel
	Max maturityLevel
}

// fullMaturityRange is PG..XXX — everything. It is BOTH the default for a fresh
// install and what every pre-existing stored NSFW mode (blur, show, and the
// normalized-away hide) migrates to, so an upgrade never makes content the user
// could already see disappear.
func fullMaturityRange() maturityRange {
	return maturityRange{Min: maturityPG, Max: maturityXXX}
}

// valid reports whether the range is a real, non-inverted band on the scale. An
// inverted range (Min > Max) is REJECTED, never silently swapped: swapping would
// quietly grant access to a band the user did not ask for, and clamping to empty
// would render a blank page that looks like a fetch failure.
func (r maturityRange) valid() bool {
	if _, ok := maturityFromSlug(r.Min.slug()); !ok {
		return false
	}
	if _, ok := maturityFromSlug(r.Max.slug()); !ok {
		return false
	}
	return r.Min <= r.Max
}

// contains reports whether a level falls inside the band. maturityUnknown never
// does (it sits above XXX), which is what makes an unrated item fail closed.
func (r maturityRange) contains(l maturityLevel) bool {
	return l >= r.Min && l <= r.Max
}

// containsBrowsingLevel is the one-call form used at every render site: take
// CivitAI's numeric level, decide whether to emit the item at all.
func (r maturityRange) containsBrowsingLevel(n int) bool {
	return r.contains(maturityFromBrowsingLevel(n))
}

// String is the persisted form, "<min>:<max>" (e.g. "pg:xxx").
func (r maturityRange) String() string {
	return r.Min.slug() + ":" + r.Max.slug()
}

// label is the compact human form used in accessible names, e.g. "PG to XXX".
func (r maturityRange) label() string {
	if r.Min == r.Max {
		return r.Min.label() + " only"
	}
	return r.Min.label() + " to " + r.Max.label()
}

// parseMaturityRange decodes the persisted/submitted "<min>:<max>" form. It
// returns ok=false for anything malformed, unknown, or INVERTED — the caller
// either falls back to the full range (a read) or rejects the request (a write).
func parseMaturityRange(s string) (maturityRange, bool) {
	min, max, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return maturityRange{}, false
	}
	lo, ok := maturityFromSlug(min)
	if !ok {
		return maturityRange{}, false
	}
	hi, ok := maturityFromSlug(max)
	if !ok {
		return maturityRange{}, false
	}
	r := maturityRange{Min: lo, Max: hi}
	if !r.valid() {
		return maturityRange{}, false
	}
	return r, true
}

// ---------------------------------------------------------------------------
// Talking to the API: the range is a CLIENT-SIDE filter over a CEILING request.
//
// 🔴 The CivitAI API cannot express a range. /api/v1/images' `nsfw` param is a
// browsing CEILING that returns a MIX at and below it. Live-probed 2026-07-31 on
// modelVersionId=3112728 (limit=100), counting (nsfwLevel|browsingLevel):
//
//	nsfw=None    -> None|1 39
//	nsfw=Soft    -> Soft|2 63, None|1 37
//	nsfw=Mature  -> Mature|4 77, Soft|2 17, X|8 1, None|1 5
//	nsfw=X       -> X|16 40, X|8 41, Mature|4 15, Soft|2 3, None|1 1
//
// So: request the ceiling that COVERS the range's Max, then filter the response
// by browsingLevel. An invalid ceiling is HTTP 400 — this param fails LOUDLY
// (unlike `tag`), so the mapping below may only ever emit a value the API
// accepts.
// ---------------------------------------------------------------------------

// imagesNSFWCeiling is the `nsfw` value /api/v1/images is asked for. The enum was
// read out of the API's own 400 body on 2026-07-31:
//
//	"Invalid option: expected one of \"None\"|\"Soft\"|\"Mature\"|\"X\"|\"Blocked\""
//
// Two consequences are load-bearing:
//
//   - There is NO "XXX" ceiling — sending one is a 400. `X` is the ceiling that
//     covers BOTH X (8) and XXX (16); that is exactly why the response mixes them
//     and why the numeric filter downstream is mandatory.
//   - "Blocked" is never emitted. It is a moderation bucket, not a step the user
//     can select, and maturityUnknown already omits anything carrying it.
//
// It is also the community cache KEY (see store migration 0018): a body fetched
// under one ceiling can never be served for another.
func (r maturityRange) imagesNSFWCeiling() string {
	switch r.Max {
	case maturityPG:
		return "None"
	case maturityPG13:
		return "Soft"
	case maturityR:
		return "Mature"
	default: // maturityX and maturityXXX both — see the doc above.
		return "X"
	}
}

// modelsNSFWFlag is the `nsfw` value /api/v1/models is asked for.
//
// That endpoint does NOT accept level names — `nsfw=Mature` is an HTTP 400 whose
// body reads `expected boolean ... expected one of "true"|"1"|"yes"|...`
// (live-probed 2026-07-31). A boolean is the ONLY lever there, so the range
// degrades to one: false restricts the feed to SFW models, true lets NSFW models
// through WITH their showcase images (without it they come back image-less and
// every card reads "No showcase images").
//
// The mapping is from the range's MAX, the same rule as the image ceiling: ask
// for everything the band could possibly need, then filter locally. Only a band
// that tops out at PG can be served by the SFW-only feed.
//
// A model is NEVER omitted by the range itself — see modelCardCarouselW. A
// model's own `nsfwLevel` is a BITMASK UNION of the levels of its images
// (measured: 31 = 1|2|4|8|16, 60 = 4|8|16|32), not a single comparable level, so
// the filtering happens per showcase IMAGE, which does carry one.
func (r maturityRange) modelsNSFWFlag() bool { return r.Max > maturityPG }
