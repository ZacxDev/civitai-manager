package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// NSFW display modes (persisted under nsfwSettingKey). blur is the default.
//
// NSFWHide is RETAINED as a constant (and the two server-side omit branches that
// key off it are kept) so the "omit NSFW server-side" capability still exists per
// the CLAUDE.md invariant — but the navbar toggle NO LONGER OFFERS hide (it is a
// 2-state Blur ⇄ Show control). normalizeNSFWMode migrates any stored "hide" to
// blur, so no user is stuck on hide and mode == NSFWHide is unreachable in
// practice (the omit branches are inert but preserved).
const (
	NSFWHide       = "hide"
	NSFWBlur       = "blur"
	NSFWShow       = "show"
	nsfwSettingKey = "nsfw_display"
)

// normalizeNSFWMode coerces a stored/submitted value to a known mode, defaulting
// to blur (the safe default: NSFW images are obscured until the user reveals one).
// A stored "hide" is MIGRATED to blur: the toggle dropped the hide state, so a
// previously-persisted hide now reads as blur everywhere (the server-side omit
// branches keyed on NSFWHide are thus never reached, but stay in place as an
// inert, preserved capability — see the const block above).
func normalizeNSFWMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case NSFWShow:
		return NSFWShow
	default: // blur (the safe default) — and "hide" migrates here too
		return NSFWBlur
	}
}

// CivitAI encodes an image's nsfwLevel as a NUMBER (a bitmask-ish severity) on
// the inline modelVersions[].images[] payload: 1=None/PG, 2=Soft/PG-13,
// 4=Mature/R, 8=X, 16=XX, 32=XXX. nsfwSafeLevel is the highest level rendered in
// the clear — only None/PG (1, and the never-observed 0) is safe; everything at
// Soft (2) and above is treated NSFW.
const nsfwSafeLevel = 1

// nsfwLevelUnknown is the sentinel the image parser assigns when an image's
// nsfwLevel is ABSENT or not an integer. It is above every real level so the
// blur/hide gate FAILS CLOSED: an image with no/garbage level is blurred (blur
// mode) and omitted (hide mode) rather than rendered un-obscured.
const nsfwLevelUnknown = 99

// isNSFWLevel reports whether a numeric CivitAI nsfwLevel should be treated as
// NSFW. Fail-closed: only an explicitly-safe level (<= nsfwSafeLevel) is safe;
// Soft (2) and above — and the nsfwLevelUnknown sentinel for an absent/garbage
// level — are NSFW.
func isNSFWLevel(level int) bool { return level > nsfwSafeLevel }

// galleryImage is one showcase image sourced from a model version's INLINE
// images[] (already present in the GetModel / GetModelVersion raw JSON) — not
// from a separate /api/v1/images call. NSFWLevel is the numeric CivitAI level
// (nsfwLevelUnknown when absent/unparseable). Meta is the flat generation
// metadata object, decoded best-effort at render time.
type galleryImage struct {
	URL       string
	NSFWLevel int
	Width     int
	Height    int
	Meta      json.RawMessage
	// Type is the media kind from the civitai payload ("image" or "video"; ""
	// when absent). A "video" tile still renders a still poster thumbnail (via
	// civitaiThumbURL, which forces anim=false), but the lightbox plays the
	// original as a <video> and the tile carries a ▶ badge + data-video marker.
	Type string
}

// isVideoType reports whether a media Type string denotes a video (case- and
// whitespace-insensitive). Centralized so the tile, lightbox marker, and parsers
// agree on what counts as a video.
func isVideoType(t string) bool {
	return strings.EqualFold(strings.TrimSpace(t), "video")
}

// rawInlineImage mirrors one object of a version's inline images[] array. The
// numeric-ish nsfwLevel is captured as raw JSON (not int) so an absent or
// non-integer value can be detected and mapped to nsfwLevelUnknown (fail closed)
// rather than silently decoding to 0 (which would read as safe).
type rawInlineImage struct {
	URL       string          `json:"url"`
	NSFWLevel json.RawMessage `json:"nsfwLevel"`
	Type      string          `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Meta      json.RawMessage `json:"meta"`
}

// toGalleryImages converts parsed inline-image objects to galleryImage values,
// mapping each nsfwLevel to its numeric level (fail-closed to nsfwLevelUnknown
// when absent/unparseable) and dropping entries with no URL.
func toGalleryImages(raws []rawInlineImage) []galleryImage {
	var out []galleryImage
	for _, ri := range raws {
		if strings.TrimSpace(ri.URL) == "" {
			continue
		}
		out = append(out, galleryImage{
			URL:       ri.URL,
			NSFWLevel: parseNSFWLevel(ri.NSFWLevel),
			Width:     ri.Width,
			Height:    ri.Height,
			Meta:      ri.Meta,
			Type:      ri.Type,
		})
	}
	return out
}

// parseNSFWLevel decodes a raw nsfwLevel value to its integer level. An absent
// (empty/null) or non-integer value → nsfwLevelUnknown (fail closed).
func parseNSFWLevel(raw json.RawMessage) int {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nsfwLevelUnknown
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nsfwLevelUnknown
	}
	return n
}

// searchImageCap bounds how many showcase images a single search card renders.
const searchImageCap = 8

// rawSearchImage mirrors one modelVersions[].images[] object in a SearchModels
// response. It captures the media Type ("image"/"video"); videos are INCLUDED —
// the tile renders a still poster (civitaiThumbURL forces anim=false) and the
// lightbox plays the original — so a video-only model still shows a card.
type rawSearchImage struct {
	URL       string          `json:"url"`
	NSFWLevel json.RawMessage `json:"nsfwLevel"`
	Type      string          `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Meta      json.RawMessage `json:"meta"`
}

// parseSearchImages extracts, per model id, a showcase-image list from a
// SearchModels raw response body (the SDK's typed ModelListItem carries no
// images, but they are present in res.Raw). It scans the model's versions in
// order and uses the FIRST version that yields at least one usable image — so a
// model whose first version has no images still shows later-version images.
// Videos are INCLUDED (the tile renders a still poster; the lightbox plays the
// original), so a video-only model still gets a card. Each model's list is capped
// at searchImageCap. This is a MANAGER-SIDE parse of data already fetched — it
// makes no extra API call. Returns a non-nil (possibly empty) map; a model with
// no usable images across any version is simply absent from it.
func parseSearchImages(raw []byte) map[int][]galleryImage {
	out := map[int][]galleryImage{}
	if len(raw) == 0 {
		return out
	}
	var body struct {
		Items []struct {
			ID            int `json:"id"`
			ModelVersions []struct {
				Images []rawSearchImage `json:"images"`
			} `json:"modelVersions"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return out
	}
	for _, it := range body.Items {
		// Show the creator's PRIMARY version's showcase — modelVersions[0], the
		// lowest-`index` version, which is exactly what the model detail page
		// defaults to (so a card and its detail page agree). Scan to the next
		// version ONLY when the primary carries no images, so the card is never
		// empty. Images keep the creator's order; videos are included (poster tile).
		var imgs []galleryImage
		for _, ver := range it.ModelVersions {
			for _, ri := range ver.Images {
				if strings.TrimSpace(ri.URL) == "" {
					continue
				}
				imgs = append(imgs, galleryImage{
					URL:       ri.URL,
					NSFWLevel: parseNSFWLevel(ri.NSFWLevel),
					Width:     ri.Width,
					Height:    ri.Height,
					Meta:      ri.Meta,
					Type:      ri.Type,
				})
				if len(imgs) >= searchImageCap {
					break
				}
			}
			if len(imgs) > 0 {
				break // primary (or first non-empty) version wins
			}
		}
		if len(imgs) > 0 {
			out[it.ID] = imgs
		}
	}
	return out
}

// parseVersionImages sources the showcase gallery from inline image data already
// fetched with the model — NEVER a separate /api/v1/images call. It prefers the
// selected version's own raw JSON (GetModelVersion) top-level images[]; when that
// carries none, it falls back to the matching version object inside the model's
// raw JSON (GetModel) modelVersions[]. Returns nil (not an error) when neither
// has any inline images.
func parseVersionImages(versionRaw, modelRaw []byte, versionID int) []galleryImage {
	if imgs := parseInlineImages(versionRaw); len(imgs) > 0 {
		return imgs
	}
	return parseModelVersionImages(modelRaw, versionID)
}

// cardCarouselImages extracts a suggestion card's showcase images from a cached
// GetModel raw body, using the SAME inline-image path the model DETAIL page uses
// (parseVersionImages against the model raw with versionID 0 → the primary
// version's images). The result is capped at searchImageCap so a card renders the
// same number of tiles a search card does. Returns nil (never an error) when the
// model carries no inline images.
func cardCarouselImages(modelRaw []byte) []galleryImage {
	imgs := parseVersionImages(nil, modelRaw, 0)
	if len(imgs) > searchImageCap {
		imgs = imgs[:searchImageCap]
	}
	return imgs
}

// parseInlineImages extracts a top-level images[] array from a raw JSON body
// (a version detail body). Returns nil when absent/unparseable.
func parseInlineImages(raw []byte) []galleryImage {
	if len(raw) == 0 {
		return nil
	}
	var body struct {
		Images []rawInlineImage `json:"images"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	return toGalleryImages(body.Images)
}

// parseModelVersionImages finds the version whose id == versionID inside a model
// detail raw body's modelVersions[] and returns its inline images[]. When
// versionID is 0 (no selection) it uses the first listed version. Returns nil
// when the version or its images are absent.
func parseModelVersionImages(modelRaw []byte, versionID int) []galleryImage {
	if len(modelRaw) == 0 {
		return nil
	}
	var body struct {
		ModelVersions []struct {
			ID     int              `json:"id"`
			Images []rawInlineImage `json:"images"`
		} `json:"modelVersions"`
	}
	if err := json.Unmarshal(modelRaw, &body); err != nil {
		return nil
	}
	for _, ver := range body.ModelVersions {
		if versionID == 0 || ver.ID == versionID {
			return toGalleryImages(ver.Images)
		}
	}
	return nil
}

// modelDetailView bundles everything the rich model page renders. Any of the
// optional pieces (Version, Images) may be zero if the corresponding API call
// failed or the data genuinely carries none — the page degrades gracefully
// rather than erroring.
type modelDetailView struct {
	Model             *civitai.ModelDetail
	Description       string // raw author HTML; sanitized at render time
	SelectedVersionID int
	Version           *civitai.ModelVersionDetail
	PublishedAt       string
	// LastUpdated is the newest modelVersions[].publishedAt across ALL versions
	// (parsed from the GetModel raw), used for the header's "Updated X ago". Zero
	// when no version carries a parseable date — the stat is then omitted.
	LastUpdated time.Time
	Images      []galleryImage
	NSFWMode    string
	// LocalVersionIDs is the set of this model's version ids the user has locally
	// (derived from local files), used to badge owned versions in the version list.
	LocalVersionIDs map[int]bool
	// VersionPublishedAt maps a version id to ITS OWN publishedAt, parsed from the
	// GetModel raw body. It is keyed by ID — never by position — because
	// modelVersions[] is ordered by the creator's `index` (primary first), NOT by
	// publish date; reading a date positionally is a documented ship-then-revert
	// bug (see the CivitAI data gotcha in CLAUDE.md). A version with no parseable
	// date is simply absent, and its tab then shows no date affordance.
	VersionPublishedAt map[int]time.Time
	// ImportedWorkflows is how many workflows in the local library were imported
	// FROM this model (store.CountWorkflowsByModel). >0 flips the import section to
	// its "already in your library" state. Only populated for Workflows-type models.
	ImportedWorkflows int
	// loadErr carries the model-load failure (used only to classify the HTTP
	// status: a not-found model → 404, anything else → 502).
	loadErr error
}

// parseModelDescription extracts the `description` field from a raw model-detail
// JSON body. ModelDetail does not carry it as a typed field, so it is read from
// the raw bytes GetModel returns. Returns "" when absent.
func parseModelDescription(raw []byte) string {
	var body struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Description
}

// parsePublishedAt best-effort reads a version's `publishedAt` timestamp from its
// raw JSON body (ModelVersionDetail does not type it). Returns "" when absent.
func parsePublishedAt(raw []byte) string {
	var body struct {
		PublishedAt string `json:"publishedAt"`
	}
	_ = json.Unmarshal(raw, &body)
	return strings.TrimSpace(body.PublishedAt)
}

// versionPublishedDate extracts the publish DATE (YYYY-MM-DD) of the given version
// id from a raw GetModel body's modelVersions[]. It is defensive: an absent field,
// undecodable bytes, or a missing version all yield "" (the popover then omits the
// date). The ISO timestamp (e.g. "2023-05-01T12:00:00.000Z") is truncated to its
// date prefix without pulling in a time parse, so a malformed value never panics.
func versionPublishedDate(raw []byte, versionID int) string {
	if len(raw) == 0 || versionID == 0 {
		return ""
	}
	var body struct {
		ModelVersions []struct {
			ID          int    `json:"id"`
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	for _, v := range body.ModelVersions {
		if v.ID != versionID {
			continue
		}
		p := strings.TrimSpace(v.PublishedAt)
		if len(p) >= 10 && p[4] == '-' && p[7] == '-' {
			return p[:10]
		}
		return p
	}
	return ""
}

// maxPublishedTime parses each publishedAt string as RFC3339 and returns the
// newest as a time.Time (zero when none parse). Empty/unparseable entries are
// skipped — defensive, never panics on malformed data. civitai timestamps carry
// fractional seconds (e.g. "2023-05-01T12:00:00.000Z"), which RFC3339 parses.
func maxPublishedTime(publishedAts []string) time.Time {
	var newest time.Time
	for _, s := range publishedAts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

// newestVersionPublishedAt scans a GetModel raw body's modelVersions[].publishedAt
// and returns the newest as a real time.Time (zero when the raw is empty,
// undecodable, or carries no parseable date). Unlike versionPublishedDate (which
// truncates to YYYY-MM-DD without parsing), this yields a time.Time for a
// relative "Updated X ago" render.
func newestVersionPublishedAt(raw []byte) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var body struct {
		ModelVersions []struct {
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return time.Time{}
	}
	ats := make([]string, 0, len(body.ModelVersions))
	for _, v := range body.ModelVersions {
		ats = append(ats, v.PublishedAt)
	}
	return maxPublishedTime(ats)
}

// versionPublishedTimes maps each version id in a GetModel raw body to ITS OWN
// publishedAt, parsed as a time.Time. It is keyed by `id` — never by array
// position — because modelVersions[] is ordered by the creator's `index`
// (primary/featured first), NOT by publish date; reading version N's date from
// position N is the documented ship-then-revert bug (see CLAUDE.md).
//
// Defensive throughout: an empty/undecodable body yields an empty (non-nil) map,
// and a version with a missing/unparseable/zero-id date is simply absent — the
// caller then renders no date affordance for it rather than a wrong one.
func versionPublishedTimes(raw []byte) map[int]time.Time {
	out := map[int]time.Time{}
	if len(raw) == 0 {
		return out
	}
	var body struct {
		ModelVersions []struct {
			ID          int    `json:"id"`
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return out
	}
	for _, v := range body.ModelVersions {
		if v.ID <= 0 {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(v.PublishedAt))
		if err != nil {
			continue
		}
		out[v.ID] = t
	}
	return out
}

// modelUpdateInfo is the per-model "last updated" summary derived from a
// SearchModels raw body: the newest version's publish time plus that version's
// NAME (both feed the search card's "Updated X ago" hover popover). Name is an
// untrusted civitai string — escape it at render time.
type modelUpdateInfo struct {
	At   time.Time
	Name string
	// VersionID is the newest version's id, used to deeplink the popover's
	// "Latest version: {name}" line to /models/{id}?version={VersionID}. Zero when
	// unknown (the line then renders as plain text, not a link).
	VersionID int
}

// newestVersionInfoByModel scans a SearchModels raw body and returns, per model
// id, the newest modelVersions[].publishedAt (as a time.Time) together with that
// version's name. A model with no parseable date is absent from the (non-nil) map.
// Defensive: an empty/undecodable raw yields an empty map, never a panic.
func newestVersionInfoByModel(raw []byte) map[int]modelUpdateInfo {
	out := map[int]modelUpdateInfo{}
	if len(raw) == 0 {
		return out
	}
	var body struct {
		Items []struct {
			ID            int `json:"id"`
			ModelVersions []struct {
				ID          int    `json:"id"`
				Name        string `json:"name"`
				PublishedAt string `json:"publishedAt"`
			} `json:"modelVersions"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return out
	}
	for _, it := range body.Items {
		var newest time.Time
		var name string
		var versionID int
		for _, v := range it.ModelVersions {
			s := strings.TrimSpace(v.PublishedAt)
			if s == "" {
				continue
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				continue
			}
			if t.After(newest) {
				newest = t
				name = v.Name
				versionID = v.ID
			}
		}
		if !newest.IsZero() {
			out[it.ID] = modelUpdateInfo{At: newest, Name: name, VersionID: versionID}
		}
	}
	return out
}

// isoDatePrefix truncates an ISO-8601 timestamp to its YYYY-MM-DD date prefix,
// defensively — an input that is not shaped like a date is returned unchanged and
// nothing ever panics.
func isoDatePrefix(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return s
}

// updatedPopBody builds the "Updated X ago" hover-popover child (.cm-updated-pop),
// mirroring .cm-vstatus-pop: an absolute "Updated {date}" title line, then
// "Latest version: {name}" and "Published {date}" lines shown only when available
// (each omitted gracefully when empty). versionName/versionDate are untrusted
// civitai strings — g.Text escapes every one; absDate is server-formatted (trusted).
// When modelID and versionID are both > 0 the "Latest version: {name}" line is a
// deeplink to that version's detail page (/models/{id}?version={vid}); otherwise it
// is plain text. The link stops click propagation so it navigates even though it
// lives inside the JS-hover-controlled popover wrapper.
func updatedPopBody(modelID, versionID int, absDate, versionName, versionDate string) g.Node {
	rows := []g.Node{
		h.Div(h.Class("cm-updated-title"), g.Text("Updated "+absDate)),
	}
	if strings.TrimSpace(versionName) != "" {
		if modelID > 0 && versionID > 0 {
			rows = append(rows, h.Div(h.A(
				h.Href(fmt.Sprintf("/models/%d?version=%d", modelID, versionID)),
				h.Class("text-indigo-300 hover:text-indigo-200"),
				g.Attr("onclick", "event.stopPropagation()"),
				g.Text("Latest version: "+versionName),
			)))
		} else {
			rows = append(rows, h.Div(g.Text("Latest version: "+versionName)))
		}
	}
	if strings.TrimSpace(versionDate) != "" {
		rows = append(rows, h.Div(h.Class("cm-updated-date"), g.Text("Published "+versionDate)))
	}
	return h.Span(h.Class("cm-updated-pop"), g.Attr("role", "tooltip"), g.Group(rows))
}

// updatedHeaderStat renders the model header's "Updated: X ago" stat as a
// hover/focus popover trigger (mirroring statInline's look) carrying updatedPopBody.
// A plain title= tooltip is kept as a harmless fallback for AT / non-hover users.
func updatedHeaderStat(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated"),
		g.Attr("tabindex", "0"),
		h.Title("Updated "+absDate),
		h.Span(h.Class("text-slate-500"), g.Text("Updated: ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// updatedCardLine renders a search card's "Updated X ago" line as a hover/focus
// popover trigger carrying updatedPopBody, with a title= fallback.
func updatedCardLine(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated text-xs text-slate-500"),
		g.Attr("tabindex", "0"),
		h.Title("Updated "+absDate),
		h.Span(g.Text("Updated "+rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// modelDetailPage renders the rich model detail page: header + stats, sanitized
// description, tags, a version selector with per-version detail, and a showcase
// image gallery with NSFW handling + a lightbox.
func modelDetailPage(v modelDetailView, sub *store.Subscription, csrf, theme, baseURL string, rail ...railData) g.Node {
	m := v.Model
	mode := normalizeNSFWMode(v.NSFWMode)

	return page(m.Name, theme, csrf, mode, railOf(rail),
		// The header is now INSIDE the version region: the header card and the
		// version tab strip are ONE card (see modelHeaderCard), and the active tab's
		// highlight has to re-render on every version swap — so the whole combined
		// card lives in the swapped container. Selecting a version htmx-swaps this
		// container's innerHTML so the URL updates (hx-push-url) and scroll is
		// preserved, without a full reload.
		h.Div(h.ID(versionRegionID), versionRegionInner(v, sub, csrf, baseURL)),
		g.If(strings.TrimSpace(v.Description) != "", modelDescriptionCard(v.Description)),
		// Tags are a compact, de-emphasized inline chip row under the description
		// (not a standalone "Tags" card).
		g.If(len(m.Tags) > 0, modelTagChips(m.Tags)),
		lightboxOverlay(),
		modelPageScript(),
		// The showcase/community carousels' prev/next buttons call cmCarouselScroll.
		libraryCarouselScript(),
	)
}

// versionRegionID is the stable container the version-dependent content lives in;
// version links htmx-swap its innerHTML (never the node itself), so the poll/swap
// target survives every version change.
const versionRegionID = "version-region"

// versionRegionInner renders the version-DEPENDENT content of the model page: the
// combined header + version-tabs card, the showcase carousel for the selected
// version, the download card, and the lazy community-feed container keyed to the
// selected version. It is rendered both inside #version-region on the full page
// AND standalone as the HX-swap response (handleModel's HX path), so a version
// change re-renders exactly this content — including the active tab highlight and
// the community feed's lazy `revealed` trigger for the new version id.
func versionRegionInner(v modelDetailView, sub *store.Subscription, csrf, baseURL string) g.Node {
	m := v.Model
	mode := normalizeNSFWMode(v.NSFWMode)
	// Order: header+tabs (one card) → larger showcase → download → community.
	return g.Group([]g.Node{
		modelHeaderCard(v, sub, csrf, baseURL),
		showcaseCard(m.ID, v.Images, mode),
		versionDownloadCard(v, csrf),
		// Workflows-type models are zips of ComfyUI workflow .json — offer a one-click
		// import into the local workflow library (Discover D2). Other model types are
		// unaffected.
		g.If(strings.EqualFold(m.Type, "Workflows"),
			workflowImportDetailCard(m.ID, csrf, v.ImportedWorkflows)),
		communityFeedContainer(m.ID, v.SelectedVersionID),
	})
}

// workflowImportDetailCard renders the workflow-import section on a Workflows-type
// model's detail page in ONE of its two states:
//
//   - imported (imported > 0): the model's workflows are already in the local
//     library, so the state-changing import CTA is REPLACED by a "View in library"
//     link into the workflows tab filtered to this model. Re-importing would only
//     report "0 imported, N already present", so offering it is a dead end.
//   - not imported: the import CTA. The old explanatory paragraph under the button
//     is deliberately gone here (the discover cards keep it — see
//     workflowImportAction); the egress it described is carried by the button's own
//     title/aria-label instead.
func workflowImportDetailCard(modelID int, csrf string, imported int) g.Node {
	if imported > 0 {
		return card(
			h.H2(h.Class("text-sm font-semibold text-slate-300 mb-2"), g.Text("Workflows")),
			h.P(h.Class("mb-2 text-xs text-slate-400"),
				g.Text(fmt.Sprintf("Already imported — %s from this model %s in your workflow library.",
					pluralWorkflows(imported), isAre(imported)))),
			h.A(
				h.Href(fmt.Sprintf("/library?tab=workflows&model=%d", modelID)),
				dataAttr("civitai-ui", "button"),
				dataAttr("variant", "filled"),
				dataAttr("size", "sm"),
				h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("→ ")),
				g.Text("View in library"),
			),
		)
	}
	return card(
		h.H2(h.Class("text-sm font-semibold text-slate-300 mb-2"), g.Text("Import workflows")),
		workflowImportAction(modelID, csrf),
	)
}

// pluralWorkflows renders "1 workflow" / "N workflows".
func pluralWorkflows(n int) string {
	if n == 1 {
		return "1 workflow"
	}
	return strconv.Itoa(n) + " workflows"
}

// isAre agrees a verb with its count. Originally written for pluralWorkflows' subject;
// the run-failure copy (run_pages.go / run_install_all.go) shares it, so keep it
// count-generic rather than workflow-specific.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// showcaseCard renders the selected version's showcase carousel (moved out of the
// header into the version region so it re-renders on a version change). The
// carousel tiles route through galleryTile → the thumbnail helper + NSFW handling
// and share the page lightbox.
func showcaseCard(modelID int, images []galleryImage, mode string) g.Node {
	return card(
		h.Div(
			h.Class("mb-2 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-sm font-semibold text-slate-300"), g.Text("Showcase images")),
		),
		// Detail-only enlargement: the .cm-showcase-lg wrapper makes the carousel
		// items taller (~22rem, see app.css) WITHOUT touching the shared
		// .cm-carousel-item height used by search/library cards, and the tiles
		// request the larger detailThumbnailWidth rendition so they stay crisp.
		h.Div(
			h.Class("cm-showcase-lg"),
			modelCardCarouselW(modelID, images, mode, detailThumbnailWidth),
		),
	)
}

// communityFeedContainer is the lazy community-feed container keyed to the
// selected version. It is fetched on `revealed` (when scrolled into view) — not
// inline — because the SearchImages call is slow; see handleModelCommunity. When
// the version region is htmx-swapped, htmx processes this fresh node and re-arms
// the `revealed` trigger for the new version id.
func communityFeedContainer(modelID, versionID int) g.Node {
	return h.Div(
		h.ID("community-feed"),
		hx("get", fmt.Sprintf("/models/%d/community?versionId=%d", modelID, versionID)),
		hx("trigger", "revealed"),
		hx("swap", "innerHTML"),
		g.Text("Loading community images…"),
	)
}

// modelCivitaiURL builds the model's canonical civitai.com URL from the
// configured base URL.
func modelCivitaiURL(baseURL string, modelID int) string {
	return fmt.Sprintf("%s/models/%d", strings.TrimRight(baseURL, "/"), modelID)
}

// modelHeaderCard renders the model header AND the version tab strip as ONE card:
// name/type/creator, the icon stats, the Subscribe affordance, the "View on
// CivitAI" link, and — along the card's bottom edge — the version tabs. Merging
// them removes a card boundary that split one logical unit (the model, and which
// of its versions you are looking at) across two panels.
//
// Consequences of the merge, both deliberate:
//   - the card lives INSIDE #version-region, because the active tab highlight must
//     re-render on every version swap;
//   - so the header re-renders on a version swap too. That is harmless (the same
//     model data, and the handler re-resolves the subscription each time).
//
// The showcase carousel is NOT here — it stays its own card below.
func modelHeaderCard(v modelDetailView, sub *store.Subscription, csrf, baseURL string) g.Node {
	m := v.Model
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	// The header's "Updated X ago" popover names the (default-selected) version and
	// its publish date when available — degrades gracefully when absent.
	verName, verDate := "", ""
	if v.Version != nil {
		verName = v.Version.Name
		verDate = isoDatePrefix(v.PublishedAt)
	}
	return card(
		h.Div(
			h.Class("flex flex-wrap items-start justify-between gap-4"),
			h.Div(
				h.H1(h.Class("text-xl font-semibold"), g.Text(m.Name)),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-2 text-sm text-slate-400"),
					badge(m.Type, "indigo"),
					g.If(m.NSFW, badge("NSFW", "red")),
					g.If(creator != "", h.A(h.Href("/creators/"+creator),
						h.Class("hover:underline"), g.Text("@"+creator))),
				),
				// Stats as icon + count, reusing the result cards' statWithIcon idiom and
				// its two glyphs (downloads / thumbs-up "likes") rather than inventing new
				// ones. The words are gone; the label survives via aria-label + title. The
				// COMMENT count is dropped entirely — it linked nowhere and the number
				// alone told the user nothing actionable.
				h.Div(
					h.Class("cm-stats mt-2 text-xs text-slate-400"),
					statWithIcon(downloadIconSVG, compactCount(m.Stats.DownloadCount), "downloads"),
					statWithIcon(thumbsUpIconSVG, compactCount(m.Stats.ThumbsUpCount), "likes"),
					// "Updated X ago" from the newest version's publish date, with a
					// hover/focus popover (absolute date + latest version name/date).
					// Omitted when no parseable date.
					g.If(!v.LastUpdated.IsZero(), updatedHeaderStat(
						m.ID, v.SelectedVersionID,
						humanSince(v.LastUpdated),
						v.LastUpdated.Local().Format("2006-01-02 15:04"),
						verName, verDate)),
				),
			),
			// Reflect the real subscription state (subscribed → "Subscribed ✓ /
			// Unsubscribe", not-subscribed → collapsed "Subscribe") and a secondary
			// "View on CivitAI" link out to the model's civitai.com page.
			h.Div(
				h.Class("flex flex-col items-end gap-2"),
				subscribeControl(m.ID, sub, csrf),
				viewOnCivitaiLink(modelCivitaiURL(baseURL, m.ID)),
			),
		),
		// The version tabs sit on the same card, along its bottom edge.
		h.Div(h.Class("mt-4"), modelVersionTabs(v)),
	)
}

// viewOnCivitaiLink renders the header's secondary "View on CivitAI" affordance:
// an anchor styled as a civitai outline button (the component CSS is
// attribute-driven, so it styles an <a> too) that opens the model's civitai.com
// page in a new tab, hardened with rel=noopener noreferrer.
//
// The URL is built from the configured BaseURL, but it is scheme-validated all
// the same (isSafeHTTPURL, as on the Apps cards): a config carrying a
// javascript:/data: base must never become an href. An unsafe/unparseable URL
// renders as no link at all.
func viewOnCivitaiLink(modelURL string) g.Node {
	if !isSafeHTTPURL(modelURL) {
		return nil
	}
	return h.A(
		h.Href(modelURL),
		h.Target("_blank"),
		g.Attr("rel", "noopener noreferrer"),
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", "outline"),
		dataAttr("size", "sm"),
		g.Text("View on CivitAI ↗"),
	)
}

func statInline(label, value string) g.Node {
	return h.Div(
		h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(value)),
	)
}

// modelDescriptionCard renders the SANITIZED description HTML. The raw author
// HTML is routed through bluemonday's UGCPolicy (see sanitize.go) before g.Raw,
// so a <script>/onerror=/javascript: in a description cannot execute.
func modelDescriptionCard(rawHTML string) g.Node {
	return card(
		sectionTitle("Description"),
		// Collapsible wrapper: default-collapsed to a max-height with a bottom fade
		// and a Read more / Show less toggle (cmToggleDesc flips data-collapsed). The
		// max-height/fade live in .cm-desc-collapsible (app.css); the sanitization is
		// unchanged — the toggle only bounds the rendered height.
		h.Div(
			h.Class("cm-desc-collapsible"),
			dataAttr("collapsed", "true"),
			h.Div(
				// cm-model-desc deterministically constrains the sanitized author HTML
				// so wide images / <pre> / <table> / long unbroken tokens cannot overflow
				// the card (see .cm-model-desc in app.css).
				// (No prose-invert: @tailwindcss/typography is not installed, so it was
				// an inert no-op class. .cm-model-desc carries the real constraints.)
				h.Class("cm-model-desc cm-desc-content max-w-none text-sm text-slate-300 space-y-2 [&_a]:text-indigo-400 [&_a]:underline"),
				g.Raw(sanitizeDescription(rawHTML)),
			),
			// Bottom fade, shown only while collapsed (CSS).
			h.Div(h.Class("cm-desc-fade"), g.Attr("aria-hidden", "true")),
			h.Button(
				h.Type("button"),
				h.Class("cm-desc-toggle"),
				g.Attr("aria-expanded", "false"),
				g.Attr("onclick", "cmToggleDesc(this)"),
				g.Text("Read more"),
			),
		),
	)
}

// modelTagChips renders the model's tags as a compact, muted inline chip row
// (see .cm-tag-chip in app.css) — small and de-emphasized rather than a full
// "Tags" card. Tag text is untrusted civitai data → g.Text escapes each one.
func modelTagChips(tags []string) g.Node {
	return h.Div(
		h.Class("flex flex-wrap items-center gap-1.5 px-1"),
		g.Map(tags, func(t string) g.Node {
			return h.Span(h.Class("cm-tag-chip"), g.Text(t))
		}),
	)
}

// versionGroupThreshold is the version count ABOVE which — when the versions span
// MORE THAN ONE distinct base model — the flat tab strip is replaced by a
// base-model selector that filters the tabs (grouped path). At or below it, or
// when every version shares ONE base model (grouping can't break that down), the
// flat scroll strip renders unchanged.
const versionGroupThreshold = 8

// versionTab renders ONE version as the tab <a>. It is the single source of the
// per-version markup shared by BOTH the flat and grouped tab paths, so every tab
// keeps the IDENTICAL htmx contract (an <a> that innerHTML-swaps #version-region,
// hx-push-url so the URL becomes /models/{id}?version={vid}, with the plain href
// as the no-JS fallback), the active highlight (cm-version-tab-active +
// aria-current), the base-model badge, and the in-library ✓.
func versionTab(v modelDetailView, ver civitai.ModelVersionSummary) g.Node {
	m := v.Model
	selected := ver.ID == v.SelectedVersionID
	cls := "cm-version-tab"
	if selected {
		cls = "cm-version-tab cm-version-tab-active"
	}
	versionHref := fmt.Sprintf("/models/%d?version=%d", m.ID, ver.ID)
	tab := []g.Node{
		h.Href(versionHref),
		hx("get", versionHref),
		hx("target", "#"+versionRegionID),
		hx("swap", "innerHTML"),
		hx("push-url", "true"),
		h.Class(cls),
		g.Attr("role", "tab"),
		h.Span(g.Text(ver.Name)),
	}
	if selected {
		tab = append(tab, g.Attr("aria-current", "true"))
	}
	if ver.BaseModel != "" {
		tab = append(tab, badge(ver.BaseModel, "blue"))
	}
	// In-library indicator: a compact green ✓ (labeled for AT). cm-ok resolves
	// the green from the civitai success token so it's genuinely green in both
	// themes, independent of the purged Tailwind build.
	if v.LocalVersionIDs[ver.ID] {
		tab = append(tab, h.Span(
			h.Class("cm-ok font-semibold"),
			h.Title("In your library"),
			g.Attr("aria-label", "In your library"),
			g.Text("✓"),
		))
	}
	// Publish-date affordance, keyed by THIS version's id.
	if pub, ok := v.VersionPublishedAt[ver.ID]; ok && !pub.IsZero() {
		tab = append(tab, versionDatePopover(pub))
	}
	return h.A(tab...)
}

// clockIconSVG is the small inline glyph the version tabs carry as their
// publish-date affordance. Static, self-contained (feather-style) markup — no
// external icon font or CDN — sized by .cm-vdate-ico in app.css and inheriting
// currentColor so both data-theme paths render.
const clockIconSVG = `<svg class="cm-vdate-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="12" cy="12" r="9"/><polyline points="12 7 12 12 15 14"/></svg>`

// versionDatePopover renders one version tab's publish-date affordance: the clock
// icon plus a hover/focus popover carrying the RELATIVE publish date ("3 weeks
// ago") and the absolute date beneath it.
//
// It reuses the page's EXISTING popover mechanism rather than adding a second
// one: the `.cm-updated` wrapper + `.cm-updated-pop` child is exactly what the
// shared hover controller in modelPageScript delegates on (its selector is
// `.cm-vstatus, .cm-updated`), so this popover gets the same open-on-hover,
// stay-open-while-hovered, 200ms-grace behavior for free, with the CSS
// :hover/:focus-within rules as the no-JS fallback.
//
// `pub` MUST be the version's own publishedAt looked up by version ID —
// modelVersions[] is ordered by the creator's `index`, not by date.
func versionDatePopover(pub time.Time) g.Node {
	rel := humanSince(pub)
	abs := pub.Local().Format("2006-01-02")
	return h.Span(
		// cm-updated → the shared popover controller + CSS; cm-vdate → the tab-local
		// sizing/color of the icon.
		h.Class("cm-updated cm-vdate"),
		g.Attr("role", "img"),
		// The date is also exposed as text for AT / non-hover users, since the icon
		// itself carries no words.
		g.Attr("aria-label", "Published "+rel),
		h.Title("Published "+rel),
		g.Raw(clockIconSVG),
		h.Span(
			h.Class("cm-updated-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text("Published "+rel)),
			h.Div(h.Class("cm-updated-date"), g.Text(abs)),
		),
	)
}

// modelVersionTabs renders the model's versions as a TAB BAR. It is NOT its own
// card — it is the bottom half of the combined header card (see modelHeaderCard),
// which lives inside #version-region so the active-tab highlight re-renders on
// every htmx swap. Two layouts:
//
//   - FLAT (default): a wrapping row of version tabs. Used when there are few
//     versions (<= versionGroupThreshold) OR all versions share one base model
//     (grouping can't help).
//   - GROUPED: when there are MANY versions (> versionGroupThreshold) AND more
//     than one distinct base model, a base-model selector (pills — one per
//     EXACT-string base model) filters the tabs; only one group's tabs show at a
//     time. The default-visible group is the one containing the active version,
//     set SERVER-SIDE (no JS needed to pick the default); pill switching is
//     client-side (cmVGroup) so the version tabs stay inside #version-region and
//     only a version click swaps the server fragment.
func modelVersionTabs(v modelDetailView) g.Node {
	m := v.Model

	// Bucket versions by EXACT base-model string, preserving first-seen order for
	// both the group order and the versions within each group.
	var order []string
	groups := map[string][]civitai.ModelVersionSummary{}
	for _, ver := range m.ModelVersions {
		if _, seen := groups[ver.BaseModel]; !seen {
			order = append(order, ver.BaseModel)
		}
		groups[ver.BaseModel] = append(groups[ver.BaseModel], ver)
	}

	// Flat strip (unchanged) unless MANY versions AND > 1 distinct base model.
	if len(m.ModelVersions) <= versionGroupThreshold || len(order) <= 1 {
		var tabs []g.Node
		for _, ver := range m.ModelVersions {
			tabs = append(tabs, versionTab(v, ver))
		}
		// A wrapping row of tabs — see .cm-version-tabs in app.css. Marked as a
		// tablist for AT, and labeled since the "Versions" heading is gone.
		return h.Div(
			h.Class("cm-version-tabs"),
			g.Attr("role", "tablist"),
			g.Attr("aria-label", "Model versions"),
			g.Group(tabs),
		)
	}

	// Grouped path. Default-visible group = the group containing the active
	// version (falls back to the first group when the selected version isn't in
	// the list). Set server-side so the correct group shows on load AND after a
	// version swap re-renders this card.
	activeGroup := order[0]
	for _, ver := range m.ModelVersions {
		if ver.ID == v.SelectedVersionID {
			activeGroup = ver.BaseModel
			break
		}
	}

	var pills []g.Node
	var panels []g.Node
	for i, bm := range order {
		key := strconv.Itoa(i)
		vers := groups[bm]
		isActive := bm == activeGroup

		label := bm
		if label == "" {
			label = "Other"
		}
		pressed := "false"
		pillCls := "cm-vgroup-pill"
		if isActive {
			pillCls = "cm-vgroup-pill cm-vgroup-pill-active"
			pressed = "true"
		}
		pills = append(pills, h.Button(
			h.Type("button"),
			h.Class(pillCls),
			dataAttr("cm-vgroup", key),
			g.Attr("aria-pressed", pressed),
			g.Attr("onclick", "cmVGroup(this)"),
			h.Span(g.Text(label)),
			h.Span(h.Class("cm-vgroup-pill-count"), g.Text(strconv.Itoa(len(vers)))),
		))

		var tabs []g.Node
		for _, ver := range vers {
			tabs = append(tabs, versionTab(v, ver))
		}
		panelAttrs := []g.Node{
			h.Class("cm-version-tabs cm-vgroup"),
			dataAttr("cm-vgroup", key),
			g.Attr("role", "tablist"),
			g.Attr("aria-label", label+" versions"),
		}
		// Only the active group's tabs are shown on load; the rest carry the HTML
		// boolean `hidden` attr (cmVGroup toggles it on a pill click).
		if !isActive {
			panelAttrs = append(panelAttrs, g.Attr("hidden", ""))
		}
		panelAttrs = append(panelAttrs, g.Group(tabs))
		panels = append(panels, h.Div(panelAttrs...))
	}

	return h.Div(
		// Wrapper cmVGroup scopes its query to (so multiple models on a page — or
		// a re-rendered region — can't cross-toggle).
		dataAttr("cm-vgroups", "true"),
		h.Div(
			h.Class("cm-vgroup-pills"),
			g.Attr("role", "group"),
			g.Attr("aria-label", "Filter versions by base model"),
			g.Group(pills),
		),
		g.Group(panels),
	)
}

// versionDownloadCard is the selected version's DOWNLOAD card (formerly the
// "Files & metadata" card). The primary action stays obvious: the file rows and
// their Download buttons render first and are always visible. Everything that is
// merely descriptive — base model, publish date, trigger words — is demoted into a
// disclosure that is COLLAPSED by default (a plain <details>, so it works with no
// JS at all and the download action is present whether it is open or shut).
func versionDownloadCard(v modelDetailView, csrf string) g.Node {
	ver := v.Version
	if ver == nil {
		return card(
			sectionTitle("Download"),
			h.P(h.Class("text-sm text-slate-500"), g.Text("Select a version to see its files.")),
		)
	}
	return card(
		h.Div(
			h.Class("mb-3 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-lg font-semibold text-slate-100"), g.Text("Download")),
			g.If(ver.BaseModel != "", badge(ver.BaseModel, "blue")),
		),
		fileList(v.Model.ID, ver.ID, ver.Files, ver.DownloadURL, csrf),
		versionMetadataReveal(v, ver),
	)
}

// versionMetadataReveal is the collapsed-by-default metadata disclosure at the
// bottom of the download card: base model, publish date, and the copy-able
// trigger-word chips. Rendered as a native <details> (no JS, keyboard-operable,
// announced as a disclosure by AT); the open/close motion is CSS in
// .cm-meta-reveal and is disabled under prefers-reduced-motion.
//
// It returns nil when the version carries none of those facts, so an empty
// disclosure never appears.
func versionMetadataReveal(v modelDetailView, ver *civitai.ModelVersionDetail) g.Node {
	var rows []g.Node
	if ver.BaseModel != "" {
		rows = append(rows, detailRow("Base model", badge(ver.BaseModel, "blue")))
	}
	if v.PublishedAt != "" {
		// The raw civitai value is a full ISO timestamp ("2023-07-29T20:50:47.173Z").
		// Show just the date — the tab popover already carries the relative age, and a
		// millisecond-precision stamp is noise in a metadata row.
		rows = append(rows, detailRow("Published",
			h.Span(h.Class("text-sm text-slate-300"), g.Text(isoDatePrefix(v.PublishedAt)))))
	}
	if len(ver.TrainedWords) > 0 {
		rows = append(rows, detailRow("Trigger words", triggerWordChips(ver.TrainedWords)))
	}
	if len(rows) == 0 {
		return nil
	}
	return h.Details(
		h.Class("cm-meta-reveal mt-3"),
		h.Summary(
			h.Class("cm-meta-summary"),
			h.Span(h.Class("cm-meta-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
			g.Text("Version metadata"),
		),
		h.Div(h.Class("cm-meta-body space-y-3"), g.Group(rows)),
	)
}

func detailRow(label string, value g.Node) g.Node {
	return h.Div(
		h.Div(h.Class("text-xs uppercase tracking-wide text-slate-500"), g.Text(label)),
		h.Div(h.Class("mt-1"), value),
	)
}

// triggerWordChips renders each trained/trigger word as a click-to-copy chip.
func triggerWordChips(words []string) g.Node {
	return h.Div(
		h.Class("flex flex-wrap gap-1.5"),
		g.Map(words, func(word string) g.Node {
			return h.Button(
				h.Type("button"),
				g.Attr("data-copy", word),
				g.Attr("onclick", "cmCopy(this)"),
				// .cm-chip now carries a REAL rest/hover pair (app.css). It replaces
				// `bg-slate-800 hover:bg-slate-700`, which was a no-op affordance:
				// tailwind.config.js maps slate 600/700/800 all to --civitai-color-border,
				// so rest and hover painted the identical color.
				h.Class("cm-chip inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs text-slate-200"),
				h.Title("Click to copy"),
				g.Text(word),
				h.Span(h.Class("text-slate-500"), g.Text("⧉")),
			)
		}),
	)
}

// fileList renders the selected version's files, each with a Download action that
// enqueues the file into the app's download queue (POST /models/{id}/download,
// CSRF-protected). versionDownloadURL is the version-level fallback used when a
// file carries no own downloadUrl.
func fileList(modelID, versionID int, files []civitai.ModelVersionFile, versionDownloadURL, csrf string) g.Node {
	if len(files) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files."))
	}
	var rows []g.Node
	for i, f := range files {
		hasURL := strings.TrimSpace(f.DownloadURL) != "" || strings.TrimSpace(versionDownloadURL) != ""
		// The FIRST file is the version's primary artifact — its Download is the
		// card's primary action and gets the filled treatment; the rest stay outline
		// so there is exactly one visually-primary control.
		variant := "outline"
		if i == 0 {
			variant = "filled"
		}
		rows = append(rows, h.Li(
			h.Class("cm-dl-file"),
			h.Span(h.Class("truncate text-sm text-slate-200"), g.Text(f.Name)),
			h.Span(h.Class("flex shrink-0 items-center gap-2 text-xs text-slate-500"),
				g.If(f.Type != "", badge(f.Type, "slate")),
				g.Text(humanBytes(int64(f.SizeKB*1024))),
				downloadFileButton(modelID, versionID, f.ID, csrf, hasURL, variant),
			),
		))
	}
	return h.Ul(h.Class("space-y-1.5"), g.Group(rows))
}

// downloadFileID is the stable element id for a file's download control, so the
// POST can outerHTML-swap just that control with its "Queued ✓" / error feedback.
func downloadFileID(modelID, versionID, fileID int) string {
	return fmt.Sprintf("dl-%d-%d-%d", modelID, versionID, fileID)
}

// downloadFileButton renders the per-file Download control. When no download URL
// is resolvable it renders a disabled note instead of a button. The POST carries
// the CSRF token (via hx-vals) and the version/file ids; the server resolves the
// destination path from the model/version/file metadata (no client path).
// variant selects the civitai button treatment ("filled" for the primary file,
// "outline" for the rest) — the htmx contract is identical either way.
func downloadFileButton(modelID, versionID, fileID int, csrf string, hasURL bool, variant string) g.Node {
	if !hasURL {
		return h.Span(h.Class("cm-disabled text-slate-500"), h.Title("No download URL available"), g.Text("no URL"))
	}
	id := downloadFileID(modelID, versionID, fileID)
	return civButton(variant, "sm", []g.Node{
		h.Type("button"),
		h.ID(id),
		hx("post", fmt.Sprintf("/models/%d/download", modelID)),
		hx("vals", fmt.Sprintf(`{"versionId":"%d","fileId":"%d","csrf_token":%q}`, versionID, fileID, csrf)),
		hx("target", "#"+id),
		hx("swap", "outerHTML"),
	}, g.Text("Download"))
}

// downloadFeedback renders the small fragment that replaces a file's Download
// control after a POST: a green "Queued ✓" on success, or a muted note (already
// queued / error). ok tints it green.
func downloadFeedback(modelID, versionID, fileID int, msg string, ok bool) g.Node {
	cls := "text-xs text-amber-400"
	if ok {
		cls = "text-xs font-medium cm-ok" // green from the civitai success token
	}
	return h.Span(h.ID(downloadFileID(modelID, versionID, fileID)), h.Class(cls), g.Text(msg))
}

// galleryTile renders one showcase image. When blur is true the image is shown
// blurred behind a click-to-reveal overlay; otherwise clicking opens the
// lightbox. Generation metadata is stashed in a hidden node (keyed by the
// caller-supplied metaID, which must be unique across the page so multiple
// galleries/carousels don't collide) the lightbox shows.
// thumbnailWidth is the pixel width requested for grid/carousel showcase tiles.
// The full-resolution original is kept for the click-to-zoom lightbox, so only
// the (many, small) grid tiles pay the reduced-bandwidth path.
const thumbnailWidth = 450

// detailThumbnailWidth is the LARGER thumbnail requested for the model DETAIL
// page's enlarged showcase carousel (~22rem tall via .cm-showcase-lg). The 450px
// card thumb looks soft at that height, so detail tiles request a bigger
// rendition. This does NOT change the shared search/library card default
// (thumbnailWidth) — only the detail showcase threads this width in.
const detailThumbnailWidth = 800

// tileThumbWidth is the width to request for one tile: the thumbnail target,
// capped to the image's own width so we never upscale a small original.
func tileThumbWidth(im galleryImage) int {
	return tileThumbWidthW(im, thumbnailWidth)
}

// tileThumbWidthW is tileThumbWidth with an explicit target width, so the detail
// showcase can request a larger rendition than the shared card default.
func tileThumbWidthW(im galleryImage, target int) int {
	if im.Width > 0 && im.Width < target {
		return im.Width
	}
	return target
}

// civitaiThumbURL rewrites an image.civitai.com URL to request an optimized,
// downscaled rendition. The civitai image CDN encodes transform params as a path
// segment between the image UUID and the filename
// (…/<bucket>/<uuid>/anim=false,width=450,optimized=true/<file>); this inserts
// that segment, or replaces an existing transform segment. Non-civitai hosts and
// unparseable/oddly-shaped URLs are returned unchanged — so this doubles as a
// host allowlist for the transform. width<=0 returns the URL unchanged.
func civitaiThumbURL(rawURL string, width int) string {
	if width <= 0 {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "image.civitai.com" {
		return rawURL
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 3 {
		return rawURL // not the expected /<bucket>/<uuid>/<file> shape — don't touch
	}
	params := fmt.Sprintf("anim=false,width=%d,optimized=true", width)
	if len(segs) >= 4 && strings.Contains(segs[2], "=") {
		// An existing transform segment sits between the uuid and the filename.
		segs[2] = params
	} else {
		// No transform segment yet — insert one before the filename.
		newSegs := make([]string, 0, len(segs)+1)
		newSegs = append(newSegs, segs[0], segs[1], params)
		segs = append(newSegs, segs[2:]...)
	}
	// Rebuild without url.String() re-encoding (which would percent-escape the
	// commas/equals in the params segment); civitai image URLs carry no query.
	return u.Scheme + "://" + u.Host + "/" + strings.Join(segs, "/")
}

func galleryTile(im galleryImage, metaID string, blur bool) g.Node {
	return galleryTileW(im, metaID, blur, thumbnailWidth)
}

// galleryTileW is galleryTile with an explicit thumbnail target width, so the
// detail showcase can request a larger rendition (detailThumbnailWidth) while the
// shared card carousel keeps the 450px default. Only the requested thumbnail
// width differs — the ORIGINAL (data-full / lightbox) is untouched.
func galleryTileW(im galleryImage, metaID string, blur bool, thumbW int) g.Node {
	imgClass := "h-full w-full cursor-zoom-in object-cover transition"
	if blur {
		imgClass += " cm-blur"
	}

	isVideo := isVideoType(im.Type)
	altText := "showcase image"
	if isVideo {
		altText = "showcase video"
	}

	img := h.Img(
		// Video tiles still show a STILL poster thumbnail: civitaiThumbURL forces
		// anim=false, so the CDN returns a poster frame even for a video source.
		h.Src(civitaiThumbURL(im.URL, tileThumbWidthW(im, thumbW))),
		h.Alt(altText),
		h.Loading("lazy"),
		// data-full is the ORIGINAL url (played as <video> for a video, shown at
		// full-res in the lightbox for an image); data-video marks video tiles so
		// cmTileClick opens the correct lightbox media node.
		g.Attr("data-full", im.URL),
		g.Attr("data-meta", metaID),
		g.If(isVideo, g.Attr("data-video", "1")),
		g.If(blur, g.Attr("data-blurred", "1")),
		g.Attr("onclick", "cmTileClick(this)"),
		h.Class(imgClass),
	)

	// True aspect ratio: shape the tile box to the image's own W/H so object-cover
	// fills it without cropping. The carousel is a fixed-HEIGHT strip (.cm-carousel-item
	// sets the height), so the box's width is derived from this ratio. When the
	// dimensions are missing/zero, fall back to a square box (aspect-square).
	wrapClass := "group relative overflow-hidden rounded-md border border-slate-800 bg-slate-900"
	children := []g.Node{}
	if im.Width > 0 && im.Height > 0 {
		children = append(children,
			h.Class(wrapClass),
			h.StyleAttr(fmt.Sprintf("aspect-ratio: %d/%d", im.Width, im.Height)),
		)
	} else {
		children = append(children, h.Class(wrapClass+" aspect-square"))
	}
	children = append(children, img)
	if isVideo {
		// A subtle ▶ badge over the poster so the tile clearly reads as a video.
		// Styled by .cm-video-badge in app.css (theme-aware; not a Tailwind util).
		children = append(children, h.Span(
			h.Class("cm-video-badge"),
			g.Attr("aria-hidden", "true"),
			g.Text("▶"),
		))
	}
	if blur {
		children = append(children, h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmReveal(this)"),
			// No cm-reveal marker class: nothing selects it (cmReveal is reached via the
			// inline onclick and walks the DOM), and it carries no rule — the button is
			// entirely utility-styled.
			h.Class("absolute inset-0 z-10 flex items-center justify-center bg-slate-950/40 text-xs font-medium text-slate-100"),
			g.Text("reveal"),
		))
	}
	children = append(children, imageMetaHidden(metaID, im))
	return h.Div(children...)
}

// imageMetaHidden renders the (hidden) generation-metadata block the lightbox
// reveals when the image is expanded.
func imageMetaHidden(metaID string, im galleryImage) g.Node {
	// Reuse the SDK's robust meta decoder (numeric-ish steps/cfg/seed handled)
	// by wrapping the inline meta bytes in an ImageItem.
	meta, state := civitai.ImageItem{Meta: im.Meta}.ParseMeta()
	var rows []g.Node
	if state == civitai.MetaOK {
		add := func(label, val string) {
			if strings.TrimSpace(val) == "" {
				return
			}
			rows = append(rows, h.Div(
				h.Class("text-xs"),
				h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
				h.Span(h.Class("text-slate-200 break-words"), g.Text(val)),
			))
		}
		add("Prompt", meta.Prompt)
		add("Negative", meta.NegativePrompt)
		add("Sampler", meta.Sampler)
		add("Steps", meta.StepsString())
		add("CFG", meta.CfgScaleString())
		add("Seed", meta.SeedString())
		add("Model", meta.Model)
	}
	if len(rows) == 0 {
		rows = append(rows, h.Div(h.Class("text-xs text-slate-500"),
			g.Text("No generation metadata for this image.")))
	}
	return h.Template(
		h.ID(metaID),
		h.Div(h.Class("space-y-1"), g.Group(rows)),
	)
}

// lightboxOverlay is the single shared full-size viewer (hidden until opened by
// cmTileClick). It shows the full image and the selected image's metadata.
func lightboxOverlay() g.Node {
	return h.Div(
		h.ID("cm-lightbox"),
		g.Attr("onclick", "cmCloseLightbox(event)"),
		h.Class("fixed inset-0 z-50 hidden items-center justify-center bg-black/80 p-4"),
		h.Div(
			h.Class("flex max-h-full w-full max-w-5xl flex-col gap-3 overflow-hidden md:flex-row"),
			h.Img(h.ID("cm-lightbox-img"), h.Alt("full image"),
				h.Class("max-h-[85vh] max-w-full rounded-md object-contain")),
			// Video counterpart, hidden until a video tile is opened. muted +
			// playsinline lets it autoplay under browser policy; loop keeps it going.
			// cmOpenLightbox/cmCloseLightbox toggle which of img/video is visible and
			// pause+clear the video on close so it stops playing.
			h.Video(
				h.ID("cm-lightbox-video"),
				g.Attr("controls", ""),
				g.Attr("loop", ""),
				g.Attr("muted", ""),
				g.Attr("playsinline", ""),
				h.Class("hidden max-h-[85vh] max-w-full rounded-md object-contain"),
			),
			h.Div(
				h.ID("cm-lightbox-meta"),
				h.Class("max-h-[85vh] w-full overflow-y-auto rounded-md bg-slate-900 p-3 md:w-80"),
			),
		),
		h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmCloseLightbox()"),
			// .cm-lightbox-close (app.css) replaces `bg-slate-800 hover:bg-slate-700`,
			// which painted the identical color at rest and on hover (slate 700/800 both
			// map to --civitai-color-border) — i.e. no hover feedback on the ONLY way
			// out of the lightbox.
			h.Class("cm-lightbox-close absolute right-4 top-4 rounded-md px-3 py-1 text-sm"),
			g.Text("Close ✕"),
		),
	)
}

// modelPageScript is the small, self-contained interaction script for the model
// page: click-to-copy chips, NSFW reveal, and the lightbox. No external JS.
func modelPageScript() g.Node {
	const js = `
function cmCopy(btn){
  var t = btn.getAttribute('data-copy') || '';
  if (navigator.clipboard) { navigator.clipboard.writeText(t); }
  var prev = btn.innerHTML;
  btn.innerHTML = 'copied ✓';
  setTimeout(function(){ btn.innerHTML = prev; }, 1200);
}
function cmReveal(btn){
  var img = btn.parentElement.querySelector('img');
  if (img){ img.classList.remove('cm-blur'); img.removeAttribute('data-blurred'); }
  btn.remove();
}
function cmTileClick(img){
  if (img.getAttribute('data-blurred')){ return; }
  cmOpenLightbox(img.getAttribute('data-full'), img.getAttribute('data-meta'), img.getAttribute('data-video'));
}
function cmOpenLightbox(url, metaId, isVideo){
  var box = document.getElementById('cm-lightbox');
  var im = document.getElementById('cm-lightbox-img');
  var vid = document.getElementById('cm-lightbox-video');
  if (isVideo && vid){
    // Show the <video>, play it (muted → autoplay allowed), hide the <img>.
    if (im){ im.classList.add('hidden'); im.src = ''; }
    vid.src = url;
    vid.classList.remove('hidden');
    vid.muted = true;
    var p = vid.play();
    if (p && p.catch){ p.catch(function(){}); }
  } else {
    // Show the <img>, pause+clear the <video>.
    if (vid){ vid.pause(); vid.removeAttribute('src'); vid.load(); vid.classList.add('hidden'); }
    if (im){ im.src = url; im.classList.remove('hidden'); }
  }
  var meta = document.getElementById('cm-lightbox-meta');
  var tpl = document.getElementById(metaId);
  meta.innerHTML = tpl ? tpl.innerHTML : '';
  box.classList.remove('hidden');
  box.classList.add('flex');
}
function cmCloseLightbox(ev){
  if (ev && ev.target && ev.target.id !== 'cm-lightbox' && ev.type === 'click') { return; }
  var box = document.getElementById('cm-lightbox');
  box.classList.add('hidden');
  box.classList.remove('flex');
  var im = document.getElementById('cm-lightbox-img');
  if (im){ im.src = ''; }
  var vid = document.getElementById('cm-lightbox-video');
  // Pause + clear the video src so closing stops playback (not just hides it).
  if (vid){ vid.pause(); vid.removeAttribute('src'); vid.load(); vid.classList.add('hidden'); }
}
function cmToggleDesc(btn){
  var box = btn.closest('.cm-desc-collapsible');
  if(!box){ return; }
  var collapsed = box.getAttribute('data-collapsed') === 'true';
  box.setAttribute('data-collapsed', collapsed ? 'false' : 'true');
  btn.setAttribute('aria-expanded', collapsed ? 'true' : 'false');
  btn.textContent = collapsed ? 'Show less' : 'Read more';
}
// Base-model version-group selector (grouped version tabs): a pill click shows
// only its group's version tabs and hides the rest — CLIENT-SIDE, so no server
// round-trip (only a version click swaps #version-region). Scoped to the pill's
// [data-cm-vgroups] wrapper. The default-visible group is server-set; this only
// handles user switching, and survives an htmx region swap because the inline
// onclick references this globally-defined function.
function cmVGroup(btn){
  var wrap = btn.closest('[data-cm-vgroups]');
  if(!wrap){ return; }
  var key = btn.getAttribute('data-cm-vgroup');
  var pills = wrap.querySelectorAll('.cm-vgroup-pill');
  for (var i=0;i<pills.length;i++){
    var on = pills[i].getAttribute('data-cm-vgroup') === key;
    pills[i].classList.toggle('cm-vgroup-pill-active', on);
    pills[i].setAttribute('aria-pressed', on ? 'true' : 'false');
  }
  var groups = wrap.querySelectorAll('.cm-vgroup');
  for (var j=0;j<groups.length;j++){
    groups[j].hidden = groups[j].getAttribute('data-cm-vgroup') !== key;
  }
}
document.addEventListener('keydown', function(e){ if (e.key === 'Escape'){ cmCloseLightbox(); } });
// cm popover hover controller: keeps the .cm-vstatus / .cm-updated popovers open
// while the mouse is over EITHER the trigger or the popover (the popover is a child
// of the trigger, so a single wrapper covers both), with a ~200ms grace delay after
// the mouse leaves both — so the now-deeplinked popover contents are reliably
// hoverable/clickable. Event delegation on document is required because these
// popovers are inserted LAZILY via htmx after page load (direct binding would miss
// them). The CSS :hover/:focus-within rules remain as fallback (keyboard a11y).
(function(){
  var timers = new WeakMap();
  function trigOf(el){ return (el && el.closest) ? el.closest('.cm-vstatus, .cm-updated') : null; }
  function openPop(trig){
    var t = timers.get(trig);
    if (t){ clearTimeout(t); timers.delete(trig); }
    trig.classList.add('cm-pop-open');
  }
  function closeSoon(trig){
    var t = timers.get(trig);
    if (t){ clearTimeout(t); }
    timers.set(trig, setTimeout(function(){ trig.classList.remove('cm-pop-open'); timers.delete(trig); }, 200));
  }
  document.addEventListener('mouseover', function(e){
    var trig = trigOf(e.target);
    if (trig){ openPop(trig); }
  });
  document.addEventListener('mouseout', function(e){
    var trig = trigOf(e.target);
    if (!trig){ return; }
    // Moving to another node still inside the trigger (incl. the popover) → stay open.
    var to = e.relatedTarget;
    if (to && trig.contains(to)){ return; }
    closeSoon(trig);
  });
})();
`
	return h.Script(g.Raw(js))
}
