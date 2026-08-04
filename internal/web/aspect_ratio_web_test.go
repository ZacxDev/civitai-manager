package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestGalleryTileAspectRatio proves a showcase tile shapes its box to the image's
// real W/H (an inline aspect-ratio style) and falls back to aspect-square when the
// dimensions are missing/zero — never panicking on absent dims.
func TestGalleryTileAspectRatio(t *testing.T) {
	withDims := renderString(t, galleryTileW(galleryImage{
		URL: "https://image.civitai.com/a.jpeg", Width: 16, Height: 9,
	}, "cm-meta-x", thumbnailWidth))
	if !strings.Contains(withDims, "aspect-ratio: 16/9") {
		t.Errorf("tile should carry the image's aspect-ratio style:\n%s", withDims)
	}
	if strings.Contains(withDims, "aspect-square") {
		t.Error("a tile with real dims should NOT use the aspect-square fallback")
	}

	noDims := renderString(t, galleryTileW(galleryImage{
		URL: "https://image.civitai.com/b.jpeg", Width: 0, Height: 0,
	}, "cm-meta-y", thumbnailWidth))
	if !strings.Contains(noDims, "aspect-square") {
		t.Error("a tile with missing dims should fall back to aspect-square")
	}
	if strings.Contains(noDims, "aspect-ratio:") {
		t.Error("a tile with missing dims should not emit an aspect-ratio style")
	}
}

// TestCarouselVariableWidthItemsAndArrows proves the carousel wraps each tile in a
// .cm-carousel-item, carries per-tile aspect-ratio styles (variable width from the
// fixed height), and still renders the prev/next scroll arrows with >1 item.
func TestCarouselVariableWidthItemsAndArrows(t *testing.T) {
	imgs := []galleryImage{
		{URL: "https://image.civitai.com/a.jpeg", NSFWLevel: 1, Width: 16, Height: 9},
		{URL: "https://image.civitai.com/b.jpeg", NSFWLevel: 1, Width: 9, Height: 16},
	}
	out := renderString(t, modelCardCarousel(7, imgs, fullMaturityRange()))
	if !strings.Contains(out, "cm-carousel-item") {
		t.Error("carousel should wrap tiles in cm-carousel-item")
	}
	if !strings.Contains(out, "aspect-ratio: 16/9") || !strings.Contains(out, "aspect-ratio: 9/16") {
		t.Errorf("each tile should carry its own aspect-ratio:\n%s", out)
	}
	// Prev/next arrows still present for a multi-item strip.
	if !strings.Contains(out, "‹") || !strings.Contains(out, "›") {
		t.Error("carousel with >1 items should render prev/next scroll arrows")
	}
	if !strings.Contains(out, "cmCarouselScroll") {
		t.Error("carousel arrows should be wired to cmCarouselScroll")
	}
}

// TestCommunityMasonryAspectRatio proves each community tile is a masonry item
// shaped to the image's real W/H, with a graceful aspect-square fallback when the
// dimensions are missing.
func TestCommunityMasonryAspectRatio(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))

	it := civitai.LeveledImage{
		ImageItem: civitai.ImageItem{
			ID: 1, URL: "https://image.civitai.com/c.jpeg", Width: 3, Height: 4,
			NSFWLevel: "None", Username: "alice",
		},
		BrowsingLevel: 1,
	}
	out := renderString(t, srv.communityImageTile(it, fullMaturityRange()))
	if !strings.Contains(out, "cm-masonry-item") {
		t.Error("community tile should be a masonry item")
	}
	if !strings.Contains(out, "aspect-ratio: 3/4") {
		t.Errorf("community tile should carry the image's aspect-ratio:\n%s", out)
	}
	if strings.Contains(out, "aspect-square") {
		t.Error("a community tile with real dims should not use the square fallback")
	}

	it0 := civitai.LeveledImage{
		ImageItem: civitai.ImageItem{
			ID: 2, URL: "https://image.civitai.com/d.jpeg",
			NSFWLevel: "None", Username: "bob",
		},
		BrowsingLevel: 1,
	}
	out0 := renderString(t, srv.communityImageTile(it0, fullMaturityRange()))
	if !strings.Contains(out0, "aspect-square") {
		t.Error("a community tile with missing dims should fall back to aspect-square")
	}
	if strings.Contains(out0, "aspect-ratio:") {
		t.Error("a community tile with missing dims should not emit an aspect-ratio style")
	}
}

// TestCommunityFeedUsesMasonryContainer proves the community feed grid was
// replaced by the CSS multi-column masonry container.
func TestCommunityFeedUsesMasonryContainer(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	out := renderString(t, srv.communityFeedFragment([]civitai.LeveledImage{
		{ImageItem: civitai.ImageItem{ID: 1, URL: "https://image.civitai.com/c.jpeg",
			Width: 2, Height: 3, NSFWLevel: "None", Username: "a"}, BrowsingLevel: 1},
	}, fullMaturityRange()))
	if !strings.Contains(out, `class="cm-masonry"`) {
		t.Errorf("community feed should render the masonry container:\n%s", out)
	}
}
