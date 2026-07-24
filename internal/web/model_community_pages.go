package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// nsfwLevelFromString maps CivitAI's STRING nsfwLevel label (as it appears on
// the /api/v1/images ImageItem — "None"/"Soft"/"Mature"/"X"/"XX"/"XXX") to the
// numeric level scale used by isNSFWLevel. It FAILS CLOSED: an empty or
// unrecognized label maps to nsfwLevelUnknown (treated NSFW), so a mislabeled or
// future-labeled image is never rendered un-obscured.
func nsfwLevelFromString(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none":
		return 1
	case "soft":
		return 2
	case "mature":
		return 4
	case "x":
		return 8
	case "xx":
		return 16
	case "xxx":
		return 32
	default:
		return nsfwLevelUnknown
	}
}

// imageReactionCount totals the positive reaction tallies on an image (likes,
// hearts, laughs, cries) — the "reactions" signal shown on a community tile.
// Comments are excluded (they are not reactions).
func imageReactionCount(st civitai.ImageStats) int {
	return st.LikeCount + st.HeartCount + st.LaughCount + st.CryCount
}

// communityFeedNote renders the (non-error) community section with a single muted
// line — used for the empty and error states so a failed lazy fetch degrades
// gracefully instead of breaking the page.
func communityFeedNote(msg string) g.Node {
	return card(
		h.H2(h.Class("text-lg font-semibold text-slate-100 mb-2"), g.Text("Community images")),
		h.P(h.Class("text-sm text-slate-500"), g.Text(msg)),
	)
}

// communityFeedFragment renders the community image grid. Each ImageItem honors
// the NSFW display mode exactly like the showcase gallery: hide OMITS the image
// server-side (its URL never reaches the DOM), blur renders it blurred behind a
// click-to-reveal overlay, show renders it plain. If every item is omitted by
// hide mode, the truthful empty note is shown.
func (s *Server) communityFeedFragment(items []civitai.ImageItem, mode string) g.Node {
	mode = normalizeNSFWMode(mode)
	var tiles []g.Node
	for _, it := range items {
		if tile := s.communityImageTile(it, mode); tile != nil {
			tiles = append(tiles, tile)
		}
	}
	if len(tiles) == 0 {
		return communityFeedNote("No community images yet.")
	}
	return card(
		h.H2(h.Class("text-lg font-semibold text-slate-100 mb-3"), g.Text("Community images")),
		h.Div(
			h.Class("grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4"),
			g.Group(tiles),
		),
	)
}

// communityImageTile renders one community image tile: a downscaled thumbnail
// that links OUT to the image's civitai.com page (new tab), captioned with the
// poster's username and reaction count. Returns nil when the image is NSFW and
// the mode is hide (so the URL is omitted from the HTML entirely).
func (s *Server) communityImageTile(it civitai.ImageItem, mode string) g.Node {
	nsfw := isNSFWLevel(nsfwLevelFromString(it.NSFWLevel))
	if nsfw && mode == NSFWHide {
		return nil // hide mode omits NSFW images entirely — URL must not reach the DOM
	}
	blur := nsfw && mode == NSFWBlur

	imgClass := "h-full w-full object-cover transition"
	if blur {
		imgClass += " blur-xl"
	}
	img := h.Img(
		h.Src(civitaiThumbURL(it.URL, thumbnailWidth)),
		h.Alt("community image"),
		h.Loading("lazy"),
		g.If(blur, g.Attr("data-blurred", "1")),
		h.Class(imgClass),
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

	children := []g.Node{
		h.Class("group relative aspect-square overflow-hidden rounded-md border border-slate-800 bg-slate-900"),
		link,
	}
	if blur {
		// Reuse the showcase cm-reveal/blur-xl pattern. The reveal must not follow
		// the tile's outbound link, so it stops the click before revealing in place.
		children = append(children, h.Button(
			h.Type("button"),
			g.Attr("onclick", "event.preventDefault();event.stopPropagation();cmReveal(this)"),
			h.Class("cm-reveal absolute inset-0 z-10 flex items-center justify-center bg-slate-950/40 text-xs font-medium text-slate-100"),
			g.Text("NSFW · click to reveal"),
		))
	}
	// Caption overlay: poster username + reaction count. Usernames are untrusted
	// civitai data — g.Text auto-escapes them.
	children = append(children, h.Div(
		h.Class("pointer-events-none absolute inset-x-0 bottom-0 z-20 flex items-center justify-between gap-2 bg-slate-950/70 px-2 py-1 text-xs text-slate-200"),
		h.Span(h.Class("truncate"), g.Text("@"+it.Username)),
		h.Span(h.Class("shrink-0"), g.Text("♥ "+strconv.Itoa(imageReactionCount(it.Stats)))),
	))
	return h.Div(children...)
}
