package web

import (
	"strings"
	"testing"
)

// TestModelHeaderCarouselReplacesGalleryCard proves the showcase carousel now
// lives INSIDE the header card and the separate gallery card is gone (images are
// not shown twice), while the description is wrapped in the cm-model-desc
// container that constrains overflow.
func TestModelHeaderCarouselReplacesGalleryCard(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	// The header region (before the Versions section) must contain the carousel.
	versionsIdx := strings.Index(body, "Versions")
	if versionsIdx < 0 {
		t.Fatal("model page missing Versions section")
	}
	header := body[:versionsIdx]
	if !strings.Contains(header, "cm-carousel") {
		t.Error("showcase carousel should render inside the header (before Versions)")
	}
	// The NSFW control moved into the header too.
	if !strings.Contains(header, "NSFW:") {
		t.Error("NSFW display control should be reachable in the header")
	}
	// The old gallery card's grid markup must be gone — images are not shown twice.
	if strings.Contains(body, "grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4") {
		t.Error("the separate gallery grid card should be removed (images shown once)")
	}
	// The description is wrapped in the overflow-constraining container.
	if !strings.Contains(body, "cm-model-desc") {
		t.Error("description should be wrapped in the cm-model-desc container")
	}
	// The carousel still shows both inline images (safe + blurred NSFW under blur).
	if !strings.Contains(body, "safe.jpeg") || !strings.Contains(body, "nsfw.jpeg") {
		t.Error("carousel should render the inline showcase images")
	}
}

// TestModelPageEmitsLazyCommunityContainer proves modelDetailPage emits the
// stable lazy #community-feed container wired to load the feed for the SELECTED
// version.
func TestModelPageEmitsLazyCommunityContainer(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, `id="community-feed"`) {
		t.Error("model page should emit the #community-feed lazy container")
	}
	// The selected version is 11 (latest / first listed) for /models/7.
	if !strings.Contains(body, `hx-get="/models/7/community?versionId=11"`) {
		t.Errorf("community container should target the selected version:\n%s", body)
	}
	if !strings.Contains(body, `hx-trigger="revealed"`) {
		t.Error("community container should lazy-load when scrolled into view (trigger=revealed)")
	}

	// Selecting an older version reloads the feed for THAT version.
	body = getModelPage(t, srv, "/models/7?version=10")
	if !strings.Contains(body, `hx-get="/models/7/community?versionId=10"`) {
		t.Error("selecting version 10 should point the feed at versionId=10")
	}
}
