package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// maturitySettingKey is the settings row holding the app-wide PG..XXX maturity
// RANGE ("<min>:<max>", see maturityRange.String). It replaced the old
// `nsfw_display` mode row in store migration 0018; an absent row means the full
// range (fullMaturityRange), which is also what every old mode migrated to.
const maturitySettingKey = "maturity_range"

// browsingLevelUnknown is the sentinel the inline-image parser assigns when an
// image's numeric level is ABSENT or not an integer. It is deliberately NOT a
// value on the maturity scale, so maturityFromBrowsingLevel maps it to
// maturityUnknown and NO range contains it: an unrated image is OMITTED rather
// than rendered on the assumption that it is tame.
const browsingLevelUnknown = 99

// galleryImage is one showcase image sourced from a model version's INLINE
// images[] (already present in the GetModel / GetModelVersion raw JSON) — not
// from a separate /api/v1/images call.
//
// NSFWLevel is CivitAI's NUMERIC level, which on THIS payload is what the
// `nsfwLevel` key literally holds (measured 2026-07-31: the inline images carry
// `"nsfwLevel": 1|2|4|8|16` and NO `browsingLevel` key at all — the opposite of
// /api/v1/images, where `nsfwLevel` is a string label and `browsingLevel` is the
// number). It feeds maturityFromBrowsingLevel unchanged; browsingLevelUnknown
// when absent/unparseable. Meta is the flat generation metadata object, decoded
// best-effort at render time.
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
// non-integer value can be detected and mapped to browsingLevelUnknown (fail
// closed) rather than silently decoding to 0.
type rawInlineImage struct {
	URL       string          `json:"url"`
	NSFWLevel json.RawMessage `json:"nsfwLevel"`
	Type      string          `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Meta      json.RawMessage `json:"meta"`
}

// toGalleryImages converts parsed inline-image objects to galleryImage values,
// mapping each nsfwLevel to its numeric level (fail-closed to
// browsingLevelUnknown when absent/unparseable) and dropping entries with no URL.
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
// (empty/null) or non-integer value → browsingLevelUnknown (fail closed).
func parseNSFWLevel(raw json.RawMessage) int {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return browsingLevelUnknown
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return browsingLevelUnknown
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
	// Maturity is the app-wide PG..XXX band. Showcase images outside it are
	// OMITTED server-side by the carousel — their URLs never reach the DOM.
	Maturity maturityRange
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
	// ImportedWorkflowList is the newest-first, CAPPED (importedWorkflowsCap) slice
	// of those same rows (store.ListWorkflowsByModel), rendered as the section's
	// carousel of cards. It is the same model_id predicate as the count, so the two
	// always describe one set — but it is a SEPARATE query, so it can legitimately
	// be empty while ImportedWorkflows is not (a failed list); the carousel then
	// renders nothing rather than an empty strip.
	ImportedWorkflowList []store.Workflow
	// UsedByWorkflows are the library workflows that REFERENCE a file belonging to
	// this model (store.ListWorkflowsUsingModel). It is a DIFFERENT relation from
	// ImportedWorkflows above — that one counts workflows imported FROM this model —
	// and the two render with different labels. Empty renders no section at all.
	//
	// It is version-INDEPENDENT (a workflow references files, not a version tab), so
	// it is loaded on the full-page path only and lives outside #version-region.
	UsedByWorkflows []store.WorkflowUsage
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
//
// ONE HOVER AFFORDANCE PER ELEMENT. An element that owns a custom popover must
// NOT also carry `title=`: the browser renders its native tooltip ON TOP of the
// popover after the OS hover delay, so the user gets two overlapping tooltips
// saying the same thing. Every `.cm-updated` / `.cm-vstatus` trigger in this app
// is therefore title-less, and each keeps an accessible name by other means — its
// own visible text, or an aria-label where the trigger is an icon (role=img). A
// `title=` is still RIGHT on an element with NO custom popover (a truncated path
// cell, a rail tile, an icon-only button): the rule is about the collision, not
// about title= being bad.
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
//
// NO title= HERE — see the "one hover affordance per element" note on
// updatedPopBody. The element's accessible name is its own visible text
// ("Updated: X ago"); the absolute date lives in the popover.
func updatedHeaderStat(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated"),
		g.Attr("tabindex", "0"),
		h.Span(h.Class("text-slate-500"), g.Text("Updated: ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// updatedCardLine renders a search card's "Updated X ago" line as a hover/focus
// popover trigger carrying updatedPopBody.
//
// NO title= HERE — see the "one hover affordance per element" note on
// updatedPopBody. Accessible name = the visible "Updated X ago" text.
func updatedCardLine(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated text-xs text-slate-500"),
		g.Attr("tabindex", "0"),
		h.Span(g.Text("Updated "+rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// modelDetailPage renders the rich model detail page: header + stats, sanitized
// description, tags, a version selector with per-version detail, and a showcase
// image gallery with NSFW handling + a lightbox.
func modelDetailPage(v modelDetailView, sub *store.Subscription, csrf, theme, baseURL string, rail ...railData) g.Node {
	m := v.Model

	return page(m.Name, theme, csrf, v.Maturity, railOf(rail),
		// The header is now INSIDE the version region: the header card and the
		// version tab strip are ONE card (see modelHeaderCard), and the active tab's
		// highlight has to re-render on every version swap — so the whole combined
		// card lives in the swapped container. Selecting a version htmx-swaps this
		// container's innerHTML so the URL updates (hx-push-url) and scroll is
		// preserved, without a full reload.
		// space-y-6 is the SECTION GUTTER, and it lives HERE rather than on the
		// cards. <main> already spaces its own direct children by space-y-6, but
		// #version-region is ONE of those children wrapping four stacked cards, so
		// everything inside it rendered flush against its neighbours. Fixing it at
		// the container keeps ONE spacing mechanism: a per-card margin would double
		// up against <main>'s gutter wherever a card is a direct child of <main>
		// instead (workflowUsageCard, the description card, …), and every future card
		// added here would have to remember to carry it. Pinned by
		// TestSectionCardsAreSpacedAtTheContainer.
		h.Div(h.ID(versionRegionID), h.Class("space-y-6"), versionRegionInner(v, sub, csrf, baseURL)),
		// Both workflow-linkage sections sit OUTSIDE #version-region, but for
		// DIFFERENT reasons and with different refresh behaviour:
		//
		//   workflowUsageCard    — genuinely per-MODEL (a local workflow references
		//                          FILES, not a version tab). Never re-renders on a
		//                          version click. Returns nil when empty.
		//   relatedWorkflowsCard — per-SELECTED-VERSION: the ecosystem comes from the
		//                          selected version's baseModel, and a model's versions
		//                          can sit on different ones (LUSTIFY!'s newest is Krea
		//                          2, its other 16 are SDXL). It is here only for
		//                          PLACEMENT; a version click re-renders it OUT OF BAND
		//                          (relatedWorkflowsOOB, wired in handleModel's HX
		//                          path). It renders an empty `hidden` container — not
		//                          nil — so that OOB target always exists; `[hidden]` is
		//                          skipped by space-y-6, so it costs no spacing.
		workflowUsageCard(m.ID, v.UsedByWorkflows),
		relatedWorkflowsCard(v),
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
// combined header + version-tabs + metadata card, the showcase carousel for the
// selected version, and the lazy community-feed container keyed to the selected
// version. (There is no longer a download card — the download action lives in the
// header's action group; see headerDownloadControl.)
//
// It is rendered both inside #version-region on the full page
// AND standalone as the HX-swap response (handleModel's HX path), so a version
// change re-renders exactly this content — including the active tab highlight and
// the community feed's lazy `revealed` trigger for the new version id.
func versionRegionInner(v modelDetailView, sub *store.Subscription, csrf, baseURL string) g.Node {
	m := v.Model
	// Order: header+tabs+download (one card) → larger showcase → community.
	return g.Group([]g.Node{
		modelHeaderCard(v, sub, csrf, baseURL),
		showcaseCard(m.ID, v.Images, v.Maturity),
		// Workflows-type models are zips of ComfyUI workflow .json — offer a one-click
		// import into the local workflow library (Discover D2). Other model types are
		// unaffected.
		g.If(civitai.IsWorkflowPost(m.Type),
			workflowImportDetailCard(m.ID, csrf, v.ImportedWorkflows, v.ImportedWorkflowList)),
		communityFeedContainer(m.ID, v.SelectedVersionID),
	})
}

// workflowImportDetailCard renders the workflow-import section on a Workflows-type
// model's detail page in ONE of its two states:
//
//   - imported (imported > 0): the model's workflows are already in the local
//     library, so the state-changing import CTA is REPLACED by the imported
//     workflows THEMSELVES — a carousel of their cards — plus a "View in library"
//     link into the workflows tab filtered to this model. Re-importing would only
//     report "0 imported, N already present", so offering it is a dead end.
//   - not imported: the import CTA. The old explanatory paragraph under the button
//     is deliberately gone here (the discover cards keep it — see
//     workflowImportAction); the egress it described is carried by the button's own
//     title/aria-label instead.
//
// `wfs` is the CAPPED (importedWorkflowsCap) newest-first slice; `imported` stays
// the TRUE total, so the sentence keeps telling the truth when the carousel is
// showing a subset. The two come from one predicate (store.CountWorkflowsByModel
// / store.ListWorkflowsByModel), so they cannot describe different sets.
// workflowImportCardID is the workflow-import section's stable in-page anchor. The
// model header's primary action links to it (workflowImportJumpLink) because on a
// workflow post the header's Download slot is replaced by the action that actually
// works, and that action lives in this card — below the showcase carousel, i.e. a
// scroll away.
const workflowImportCardID = "workflow-import"

// workflowImportJumpLink is the model header's primary action on a Workflows post:
// a plain in-page anchor to the import card, styled as the filled button that used
// to be Download. No JS, no POST, no CSRF — it only moves the viewport, so it
// cannot be the "second button that looks equivalent but doesn't work".
//
// The label follows the card's own state so the two can never contradict each
// other: nothing imported yet → "Import workflows"; already imported → "View
// workflows", because re-importing would only report "0 imported, N already
// present" (see workflowImportDetailCard).
func workflowImportJumpLink(imported int) g.Node {
	label := "Import workflows"
	aria := "Go to the workflow import section"
	if imported > 0 {
		label = "View workflows"
		aria = "Go to the workflows imported from this model"
	}
	return h.A(
		h.Href("#"+workflowImportCardID),
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", "filled"),
		dataAttr("size", "sm"),
		g.Attr("aria-label", aria),
		g.Text(label),
	)
}

func workflowImportDetailCard(modelID int, csrf string, imported int, wfs []store.Workflow) g.Node {
	if imported > 0 {
		return card(
			h.ID(workflowImportCardID),
			h.H2(h.Class("text-sm font-semibold text-slate-300 mb-2"), g.Text(workflowImportCardHeading)),
			h.P(h.Class("mb-2 text-xs text-slate-400"),
				g.Text(fmt.Sprintf("Already imported — %s from this model %s in your workflow library.",
					pluralWorkflows(imported), isAre(imported)))),
			importedWorkflowsCarousel(wfs),
			importedWorkflowsOverflowNote(imported, len(wfs)),
			// The link through to the library SURVIVES the carousel — it is no longer
			// the only affordance, but it is still the way to the full, filterable,
			// mutable list, and the only way to the ones past the cap.
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
	// Heading LEFT, action RIGHT — the same header row showcaseCard and the related-
	// workflows card already use, so this cannot drift into a one-off. The action
	// container stays INSIDE the row on purpose: it is the hx-swap target, so after
	// the import the result line lands beside the stable heading rather than under a
	// heading that has just stopped describing the card.
	return card(
		h.ID(workflowImportCardID),
		h.Div(
			h.Class("mb-2 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-sm font-semibold text-slate-300"), g.Text(workflowImportCardHeading)),
			workflowImportAction(modelID, csrf),
		),
	)
}

// workflowImportCardHeading names the workflows this model POST unpacks into your
// library. It is ONE constant used by both states on purpose.
//
// It used to be "Import workflows" before importing and "Workflows" after, so the
// card appeared to rename itself; the verb now lives on the button, where a verb
// belongs, and the heading is a stable noun.
//
// "from this model" is not padding. A model page now carries THREE sections whose
// headings begin with "Workflows" — "Workflows that use this model" (local library,
// matched by file) and "Workflows for <ecosystem>" (remote, by base model) both
// landed in v0.1.88 — so a bare "Workflows" would be the ambiguous one of three
// siblings rather than the clearest.
const workflowImportCardHeading = "Workflows from this model"

// importedWorkflowsCap bounds how many imported-workflow cards the model detail
// page paints. A single Workflows model routinely ships a pack of dozens of
// .json files and every one becomes a row; this section is a "here is what you
// already have" glance, not the library. Past the cap the "View in library" link
// (plus importedWorkflowsOverflowNote) carries the user to the full list. It is
// also the SQL LIMIT — see loadModelView — so the rows past it are never
// fetched, let alone rendered.
const importedWorkflowsCap = 8

// importedWorkflowsCarousel renders the imported workflows as compact cards in
// the shared card carousel.
//
// It renders NOTHING for an empty slice — no heading, no empty strip, no stray
// wrapper — and returns a nil node so gomponents skips it entirely. That is
// reachable in production even when imported > 0: the count and the list are two
// queries, and a failed list leaves the sentence with no rows behind it.
//
// The cards are workflowCardCompact — the shared renderer's compact variant; see
// its doc for what it drops and why. Notably it carries NO image tiles, so this
// strip has none of the NSFW-reveal / video-badge / inner-carousel-button
// decoration that escapes `.cm-carousel-wrap` (which is `position: relative;
// z-index: auto`, deliberately NOT a stacking context) and paints over a
// neighbouring transformed .cm-lift card. The v0.1.82 hazard is absent here by
// construction rather than by z-index.
func importedWorkflowsCarousel(wfs []store.Workflow) g.Node {
	if len(wfs) == 0 {
		return nil
	}
	cards := make([]g.Node, 0, len(wfs))
	for _, wf := range wfs {
		cards = append(cards, workflowCardCompact(wf))
	}
	return cardCarousel(cards)
}

// importedWorkflowsOverflowNote states, honestly and only when true, that the
// carousel is a subset — so 8 cards under a sentence reading "23 workflows"
// never looks like a bug. Nil when nothing was withheld.
func importedWorkflowsOverflowNote(total, shown int) g.Node {
	if shown <= 0 || total <= shown {
		return nil
	}
	return h.P(h.Class("mb-2 text-xs text-slate-500"),
		g.Text(fmt.Sprintf("Showing the %d most recent.", shown)))
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
func showcaseCard(modelID int, images []galleryImage, mr maturityRange) g.Node {
	return card(
		h.Div(
			h.Class("mb-2 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-sm font-semibold text-slate-300"), g.Text("Showcase images")),
		),
		showcaseCarousel(modelID, images, mr),
	)
}

// showcaseCardUntitled is showcaseCard WITHOUT the "Showcase images" caption, for a
// surface where the label is redundant chrome: the workflow detail page shows these
// images directly under the workflow's own <h1> and has no other pictures on the
// page, so naming them added a heading and told the reader nothing.
//
// It shares the carousel with showcaseCard (one renderer, two headings) so the two
// surfaces cannot drift in sizing, NSFW handling or lightbox wiring.
func showcaseCardUntitled(modelID int, images []galleryImage, mr maturityRange) g.Node {
	return card(showcaseCarousel(modelID, images, mr))
}

// showcaseCarousel is the shared body of both showcase cards.
//
// Detail-only enlargement: the .cm-showcase-lg wrapper makes the carousel items
// taller (~22rem, see app.css) WITHOUT touching the shared .cm-carousel-item height
// used by search/library cards, and the tiles request the larger
// detailThumbnailWidth rendition so they stay crisp.
func showcaseCarousel(modelID int, images []galleryImage, mr maturityRange) g.Node {
	return h.Div(
		h.Class("cm-showcase-lg"),
		modelCardCarouselW(modelID, images, mr, detailThumbnailWidth),
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
			// The header's ACTION group, most-primary first: the selected version's
			// download (moved here from the retired standalone download card — see
			// headerDownloadControl), the real subscription state (subscribed →
			// "Subscribed ✓ / Unsubscribe", not-subscribed → collapsed "Subscribe"),
			// and a secondary "View on CivitAI" link out to the model's civitai.com
			// page.
			h.Div(
				h.Class("flex flex-col items-end gap-2"),
				headerDownloadControl(v, csrf),
				subscribeControl(m.ID, sub, csrf, civitai.IsWorkflowPost(m.Type)),
				viewOnCivitaiLink(modelCivitaiURL(baseURL, m.ID)),
			),
		),
		// The version tabs sit on the same card, along its bottom edge.
		h.Div(h.Class("mt-4"), modelVersionTabs(v)),
		// …and DIRECTLY under them, the selected version's metadata disclosure.
		//
		// WHY HERE. It used to sit at the bottom of the download card, which this
		// change retires. Base model / publish date / trigger words are facts about
		// the SELECTED VERSION, and the control that selects that version is the tab
		// strip immediately above — so "which version, and what is it" stays one
		// unit. It also has to stay inside #version-region (this card already is), or
		// a version swap would leave stale metadata on screen. Parking it on the
		// showcase card instead would have attached version facts to images.
		//
		// Still a native <details>: click / Enter / Space, announced as a disclosure,
		// zero JS. Nil when the version carries none of those facts.
		headerVersionMetadata(v),
	)
}

// headerVersionMetadata is versionMetadataReveal guarded for a nil selected
// version. It exists because g.If evaluates its node argument EAGERLY — writing
// g.If(v.Version != nil, versionMetadataReveal(v, v.Version)) inline would
// dereference a nil version before the condition is ever consulted.
func headerVersionMetadata(v modelDetailView) g.Node {
	if v.Version == nil {
		return nil
	}
	return versionMetadataReveal(v, v.Version)
}

// headerDownloadControl renders the selected version's DOWNLOAD affordance inside
// the model header's action group. It replaces the standalone download card
// (versionDownloadCard, retired): the page's primary action now sits with the
// other header actions instead of a scroll below them.
//
// TWO SHAPES, decided purely by file count — the header is a fixed-width action
// column and has to stay one no matter how many files a version ships:
//
//   - EXACTLY ONE file → the direct Download button. ONE click, no menu, no
//     intermediate step; a menu here would add a step to the common case for
//     nothing.
//   - MORE THAN ONE → a single trigger opening a popover that lists every file
//     with its size and type. That bounds the header's width at any file count
//     (some versions ship a dozen files).
//
// ZERO files → NO control at all, not an empty menu. That deliberately covers the
// "version carries a DownloadURL but no file list" case as well: POST
// /models/{id}/download REQUIRES a fileId > 0 (handleModelDownload answers
// "Invalid request" for fileID <= 0), so a version-level URL with no file rows is
// NOT actionable through this endpoint. A button that could only ever fail would
// be a lie; rendering nothing is the honest degradation.
//
// A WORKFLOW POST never gets a Download button, whatever its file list says. Its
// versions carry an Archive .zip with a real SHA256, a downloadUrl and a
// primary: true flag — so every check above passes and the button used to render
// byte-identically to a checkpoint's — but `.zip` is not in
// library.DefaultExtensions, so those bytes would never be scanned, counted,
// deduped or quarantined. The action that DOES work is Import, and it is already
// on this page, so the control is REPOINTED at it rather than merely hidden:
// leaving the header's primary slot empty on a page whose whole point is the
// import card would be a worse answer than a wrong button was. Server-side,
// handleModelDownload refuses the same case — a button disappearing is not an
// endpoint refusing.
func headerDownloadControl(v modelDetailView, csrf string) g.Node {
	ver := v.Version
	if v.Model == nil {
		return nil
	}
	if civitai.IsWorkflowPost(v.Model.Type) {
		return workflowImportJumpLink(v.ImportedWorkflows)
	}
	if ver == nil || len(ver.Files) == 0 {
		return nil
	}
	modelID := v.Model.ID

	if len(ver.Files) == 1 {
		f := ver.Files[0]
		return downloadFileButton(modelID, ver.ID, f.ID, csrf,
			fileHasDownloadURL(f, ver.DownloadURL), "filled")
	}

	// A native <details>, matching versionMetadataReveal / versionTabsOlder in this
	// same file rather than inventing a second popover mechanism. Deliberately NOT
	// the shared .cm-updated HOVER popover: this panel holds real buttons, and a
	// hover-scoped surface that closes when the pointer strays is hostile to
	// clicking one. <details> opens on click AND on Enter/Space with the summary
	// focused, exposes an expanded state to AT, and needs no JS at all.
	return h.Details(
		h.Class("cm-dl-menu"),
		h.Summary(
			h.Class("cm-dl-menu-sum"),
			// The civitai button CSS is attribute-driven, so it styles a <summary>
			// exactly as it styles a <button> — no NEW coloured pair enters
			// contrast_web_test.go's table.
			dataAttr("civitai-ui", "button"),
			dataAttr("variant", "filled"),
			dataAttr("size", "sm"),
			// The count is IN the visible label (not only in an aria-label), so the
			// affordance says how much it is hiding — same rule as versionTabsOlder.
			g.Text(fmt.Sprintf("Download (%d files)", len(ver.Files))),
			h.Span(h.Class("cm-dl-menu-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
		),
		h.Div(
			h.Class("cm-dl-menu-pop"),
			fileList(modelID, ver.ID, ver.Files, ver.DownloadURL, csrf),
		),
	)
}

// fileHasDownloadURL reports whether a file can be enqueued at all: it has its own
// downloadUrl, or the version-level one handleModelDownload falls back to.
func fileHasDownloadURL(f civitai.ModelVersionFile, versionDownloadURL string) bool {
	return strings.TrimSpace(f.DownloadURL) != "" || strings.TrimSpace(versionDownloadURL) != ""
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

// versionTabVisibleN is how many version tabs render as a plain strip before the
// remainder folds into ONE "N older" disclosure.
//
// WHY 6. Measured on the real /models/4384 (31 versions) in a browser at a
// 1121px-wide tab strip: a tab is 107px at its narrowest, 250px at its widest,
// mean 181px. 1121 / (181 + 4px gap) ≈ 6.0, so six average tabs are exactly one
// row at a normal desktop width, and the strip degrades to two rows — not seven,
// which is what 31 tabs actually produced — when the names run long or the window
// is narrow. Anything below ~4 stops showing enough recent history to be useful
// as a strip; anything above ~8 is back to wrapping by default.
const versionTabVisibleN = 6

// splitVersionTabs decides which of a version list renders as plain tabs and
// which folds behind the "N older" disclosure.
//
// 🔴 NEWEST IS A DATE QUESTION, NOT A POSITION ONE. modelVersions[] is ordered by
// the creator's `index` — primary/featured FIRST — not by publish date, so
// vers[0] is the version the detail page defaults to and says nothing about
// recency. Reading recency positionally is a documented ship-then-revert in this
// repo (see the CivitAI data gotcha in CLAUDE.md). Ranking therefore reads
// VersionPublishedAt, which is keyed by version ID. A version with no parseable
// date ranks LAST — we cannot claim it is recent — with the array order as a
// stable tiebreak so the output is deterministic.
//
// THE SELECTED VERSION IS ALWAYS VISIBLE, even when it is genuinely old: a
// collapsed disclosure that swallowed the active tab would leave the strip with
// no highlighted tab at all, and the user would have no idea where they are. It
// DISPLACES the oldest otherwise-visible version rather than growing the strip,
// so the row length stays put.
//
// Both slices keep the ORIGINAL array order — only membership is date-decided —
// so the strip still reads in the order the creator arranged it, as before.
func splitVersionTabs(v modelDetailView, vers []civitai.ModelVersionSummary) (visible, older []civitai.ModelVersionSummary) {
	if len(vers) <= versionTabVisibleN {
		return vers, nil
	}

	dateOf := func(i int) (time.Time, bool) {
		t, ok := v.VersionPublishedAt[vers[i].ID]
		return t, ok && !t.IsZero()
	}

	// Rank positions newest-first. SliceStable keeps the array order for equal
	// dates and for the undated tail.
	rank := make([]int, len(vers))
	for i := range rank {
		rank[i] = i
	}
	sort.SliceStable(rank, func(a, b int) bool {
		ta, oka := dateOf(rank[a])
		tb, okb := dateOf(rank[b])
		if oka != okb {
			return oka // dated versions outrank undated ones
		}
		if !oka {
			return false // both undated → leave in array order
		}
		return ta.After(tb)
	})

	keep := make(map[int]bool, versionTabVisibleN)
	for _, i := range rank[:versionTabVisibleN] {
		keep[vers[i].ID] = true
	}
	// Force the selected version in (only if it is actually in THIS list — the
	// grouped path calls us per base-model group, and the selection lives in
	// exactly one of them).
	if v.SelectedVersionID != 0 && !keep[v.SelectedVersionID] {
		for _, ver := range vers {
			if ver.ID != v.SelectedVersionID {
				continue
			}
			// rank[versionTabVisibleN-1] is the OLDEST version we were keeping.
			delete(keep, vers[rank[versionTabVisibleN-1]].ID)
			keep[v.SelectedVersionID] = true
			break
		}
	}

	for _, ver := range vers {
		if keep[ver.ID] {
			visible = append(visible, ver)
		} else {
			older = append(older, ver)
		}
	}
	return visible, older
}

// versionTabNodes renders one version list as tab nodes, folding everything past
// versionTabVisibleN into the trailing "N older" disclosure. Shared by BOTH the
// flat and the grouped layout so the two can never diverge.
func versionTabNodes(v modelDetailView, vers []civitai.ModelVersionSummary) []g.Node {
	visible, older := splitVersionTabs(v, vers)
	nodes := make([]g.Node, 0, len(visible)+1)
	for _, ver := range visible {
		nodes = append(nodes, versionTab(v, ver))
	}
	if len(older) > 0 {
		nodes = append(nodes, versionTabsOlder(v, older))
	}
	return nodes
}

// versionTabsOlder is the ONE disclosure holding every version that did not make
// the visible strip.
//
// A plain <details>/<summary>: it opens on click AND on Enter/Space with the
// element focused, it is exposed to AT as a disclosure with an expanded state,
// and it needs ZERO JavaScript — matching versionMetadataReveal's idiom in this
// same file rather than inventing a second mechanism. The count is IN the summary
// text (not only in an aria-label) so the affordance says how much it is hiding.
func versionTabsOlder(v modelDetailView, older []civitai.ModelVersionSummary) g.Node {
	tabs := make([]g.Node, 0, len(older))
	for _, ver := range older {
		tabs = append(tabs, versionTab(v, ver))
	}
	label := fmt.Sprintf("%d older", len(older))
	return h.Details(
		h.Class("cm-vmore"),
		h.Summary(
			h.Class("cm-vmore-sum"),
			// Same chevron idiom as .cm-meta-summary; the CSS rotates it when open.
			h.Span(h.Class("cm-vmore-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
			g.Attr("aria-label", fmt.Sprintf("Show %d older version%s", len(older), plural(len(older)))),
			g.Text(label),
		),
		h.Div(h.Class("cm-vmore-tabs"), g.Group(tabs)),
	)
}

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
		// itself carries no words. role=img means aria-label IS the accessible name,
		// so there is nothing left for a title= to contribute — and adding one would
		// stack the NATIVE tooltip on top of this popover (see updatedPopBody).
		g.Attr("aria-label", "Published "+rel),
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
		tabs := versionTabNodes(v, m.ModelVersions)
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

		tabs := versionTabNodes(v, vers)
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

// versionMetadataReveal is the collapsed-by-default metadata disclosure carrying
// the selected version's base model, publish date, and copy-able trigger-word
// chips. It used to sit at the bottom of the standalone download card; that card
// is retired (its action moved into the header — see headerDownloadControl) and
// this disclosure now renders directly under the version tab strip in
// modelHeaderCard. See the WHY-HERE note there.
//
// Rendered as a native <details> (no JS, keyboard-operable, announced as a
// disclosure by AT); the open/close motion is CSS in
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
		h.Class("cm-meta-reveal mt-4"),
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
//
// It is the body of the header download MENU (headerDownloadControl's >1-file
// shape). The single-file shape never reaches here — it renders the bare button —
// and the zero-file case is refused by the caller, so the nil return below is a
// defensive floor, not a rendered "No files." state.
func fileList(modelID, versionID int, files []civitai.ModelVersionFile, versionDownloadURL, csrf string) g.Node {
	if len(files) == 0 {
		return nil
	}
	var rows []g.Node
	for i, f := range files {
		hasURL := fileHasDownloadURL(f, versionDownloadURL)
		// The FIRST file is the version's primary artifact — its Download is the
		// menu's primary action and gets the filled treatment; the rest stay outline
		// so there is exactly one visually-primary control.
		variant := "outline"
		if i == 0 {
			variant = "filled"
		}
		rows = append(rows, h.Li(
			h.Class("cm-dl-file"),
			// The full name as the ROW's tooltip, so a truncated one is still
			// recoverable. It sits on the <li>, NOT on the name <span>, deliberately:
			// TestLongUntrustedStringsCanBreak exempts any element carrying `title=`,
			// so putting it on the span would silently exempt the very element whose
			// truncate/min-w-0 pairing that test exists to enforce (verified: with the
			// title on the span, deleting min-w-0 went completely undetected).
			h.Title(f.Name),
			// min-w-0 is REQUIRED alongside truncate, not decoration: this is a flex
			// item, and a flex item's min-width:auto keeps its min-content width (the
			// whole unbroken filename) as a floor, so truncate alone would blow the
			// row — and the menu — past the viewport.
			h.Span(h.Class("min-w-0 truncate text-sm text-slate-200"), g.Text(f.Name)),
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

// galleryTile renders one showcase image. Clicking it opens the lightbox.
//
// There is NO blur/reveal path any more: an image the user's maturity range does
// not cover is never handed to this function at all (the carousel omits it
// server-side), so everything that reaches a tile renders PLAIN. Blur used to be
// a browser-side CSS filter that still shipped the bytes.
// Generation metadata is stashed in a hidden node (keyed by the
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

func galleryTile(im galleryImage, metaID string) g.Node {
	return galleryTileW(im, metaID, thumbnailWidth)
}

// galleryTileW is galleryTile with an explicit thumbnail target width, so the
// detail showcase can request a larger rendition (detailThumbnailWidth) while the
// shared card carousel keeps the 450px default. Only the requested thumbnail
// width differs — the ORIGINAL (data-full / lightbox) is untouched.
func galleryTileW(im galleryImage, metaID string, thumbW int) g.Node {
	imgClass := "h-full w-full cursor-zoom-in object-cover transition"

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
// page: click-to-copy chips and the lightbox. No external JS.
//
// There is no cmReveal any more — nothing is blurred, because content outside the
// maturity range is omitted server-side instead of obscured client-side.
func modelPageScript() g.Node {
	const js = `
function cmCopy(btn){
  var t = btn.getAttribute('data-copy') || '';
  if (navigator.clipboard) { navigator.clipboard.writeText(t); }
  var prev = btn.innerHTML;
  btn.innerHTML = 'copied ✓';
  setTimeout(function(){ btn.innerHTML = prev; }, 1200);
}
function cmTileClick(img){
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
