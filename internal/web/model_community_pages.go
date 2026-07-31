package web

import (
	"fmt"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// 🔴 THERE IS DELIBERATELY NO nsfwLevelFromString HERE ANY MORE.
//
// This file used to map /api/v1/images' STRING `nsfwLevel` label onto a numeric
// scale. That mapping is UNSOUND and was removed with the maturity range: the
// endpoint labels BOTH of its top two levels `"X"`. Measured 2026-07-31 on
// nsfw=X&limit=100 — 41 items at browsingLevel 8 and 40 at browsingLevel 16, all
// labelled "X" — so a string mapping cannot tell X from XXX, which makes both
// "X only" and "up to X" lies. The tiles read civitai.LeveledImage.BrowsingLevel,
// the NUMBER, instead. Do not reintroduce a label-based level.

// imageReactionCount totals the positive reaction tallies on an image (likes,
// hearts, laughs, cries) — the "reactions" signal shown on a community tile.
// Comments are excluded (they are not reactions).
func imageReactionCount(st civitai.ImageStats) int {
	return st.LikeCount + st.HeartCount + st.LaughCount + st.CryCount
}

// communityFeedAbsent is what the lazy community fragment renders when there is
// nothing to show: NOTHING AT ALL.
//
// The section used to degrade to a card carrying the "Community images" heading
// plus a muted "No community images yet." / "Couldn't load…" line. A heading over
// a permanent blank is worse than no section: on a model with no community
// images it is a dead panel the user has to read and dismiss on every visit. So
// the whole section — heading included — is omitted, and the lazy container
// (#community-feed) is simply left empty. Fetch failures are still logged
// server-side; they just don't leave a scar on the page.
func communityFeedAbsent() g.Node { return g.Text("") }

// communityFeedFragment renders the community image grid, honoring the app-wide
// maturity range exactly like the showcase gallery: an image outside the band is
// OMITTED server-side (its URL never reaches the DOM); everything inside renders
// plain. If every item is omitted, the section is omitted entirely.
//
// It takes MORE items than it renders — see communityFetchLimit. The upstream
// `nsfw` param is a CEILING that returns a mix at and below it, so a narrow band
// is a fraction of the response; the handler over-fetches and this clamps to
// communityPageSize so a full page still fills.
func (s *Server) communityFeedFragment(items []civitai.LeveledImage, mr maturityRange) g.Node {
	var tiles []g.Node
	for _, it := range items {
		if len(tiles) >= communityPageSize {
			break // clamp: the over-fetch feeds the filter, not the page
		}
		if tile := s.communityImageTile(it, mr); tile != nil {
			tiles = append(tiles, tile)
		}
	}
	// Zero renderable tiles → the section is omitted entirely (no heading, no empty
	// state). See communityFeedAbsent.
	if len(tiles) == 0 {
		return communityFeedAbsent()
	}
	return card(
		h.H2(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text("Community images")),
		// CSS multi-column masonry (.cm-masonry): variable-height, true-aspect-ratio
		// tiles flow into columns (2 on mobile, 3–4 at wider widths) — see app.css.
		h.Div(
			h.Class("cm-masonry"),
			g.Group(tiles),
		),
	)
}

// communityImageTile renders one community image tile: a downscaled thumbnail
// that links OUT to the image's civitai.com page (new tab), captioned with the
// poster's username and reaction count. Returns nil when the image's maturity
// level falls outside the range — the URL is then omitted from the HTML entirely,
// never merely styled differently.
//
// It reads BrowsingLevel (the number), never NSFWLevel (the string label): see
// the note at the top of this file.
func (s *Server) communityImageTile(it civitai.LeveledImage, mr maturityRange) g.Node {
	if !mr.containsBrowsingLevel(it.BrowsingLevel) {
		return nil // outside the range — the URL must not reach the DOM
	}

	img := h.Img(
		h.Src(civitaiThumbURL(it.URL, thumbnailWidth)),
		h.Alt("community image"),
		h.Loading("lazy"),
		h.Class("h-full w-full object-cover transition"),
	)

	// The image links out to civitai (external, new tab) — NOT the internal
	// lightbox. rel=noopener noreferrer per the standard new-tab hardening.
	link := h.A(
		h.Href(fmt.Sprintf("%s/images/%d", s.cfg.BaseURL, it.ID)),
		h.Target("_blank"),
		g.Attr("rel", "noopener noreferrer"),
		h.Class("absolute inset-0 block"),
		img,
	)

	// True aspect ratio: shape the tile to the image's own W/H (object-cover fills
	// it without cropping); the variable heights are what make the columns read as
	// masonry. Fall back to a square box when the dimensions are missing/zero.
	wrapClass := "cm-masonry-item group relative overflow-hidden rounded-md border border-slate-800 bg-slate-900"
	children := []g.Node{}
	if it.Width > 0 && it.Height > 0 {
		children = append(children,
			h.Class(wrapClass),
			h.StyleAttr(fmt.Sprintf("aspect-ratio: %d/%d", it.Width, it.Height)),
		)
	} else {
		children = append(children, h.Class(wrapClass+" aspect-square"))
	}
	children = append(children, link)
	// Caption overlay: poster username + reaction count. Usernames are untrusted
	// civitai data — g.Text auto-escapes them.
	children = append(children, h.Div(
		h.Class("pointer-events-none absolute inset-x-0 bottom-0 z-20 flex items-center justify-between gap-2 bg-slate-950/70 px-2 py-1 text-xs text-slate-200"),
		h.Span(h.Class("truncate"), g.Text("@"+it.Username)),
		h.Span(h.Class("shrink-0"), g.Text("♥ "+compactCount(imageReactionCount(it.Stats)))),
	))
	return h.Div(children...)
}
