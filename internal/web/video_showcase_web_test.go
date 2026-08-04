package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestParseModelVersionImagesPreservesType proves the media Type threads from a
// GetModel raw body's inline images[] through to the parsed galleryImage values.
func TestParseModelVersionImagesPreservesType(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"modelVersions": []any{
			map[string]any{"id": 7, "images": []any{
				map[string]any{"url": "https://image.civitai.com/a.jpeg", "type": "image", "nsfwLevel": 1},
				map[string]any{"url": "https://image.civitai.com/b.mp4", "type": "video", "nsfwLevel": 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	imgs := parseModelVersionImages(raw, 7)
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d", len(imgs))
	}
	if isVideoType(imgs[0].Type) {
		t.Errorf("first item should be an image, Type=%q", imgs[0].Type)
	}
	if !isVideoType(imgs[1].Type) {
		t.Errorf("second item should carry a video Type, got %q", imgs[1].Type)
	}
}

// TestGalleryTileVideoMarkers proves a video tile carries data-video="1", the ▶
// badge, a still poster src (anim=false via civitaiThumbURL) and data-full = the
// ORIGINAL video url; an image tile carries none of the video markers.
func TestGalleryTileVideoMarkers(t *testing.T) {
	const uuid = "https://image.civitai.com/xG1nkqKTMzGDvpLrqFT7WA/ad0eb2e0-c228-4131-956d-ca01b95552d3"
	vidURL := uuid + "/clip.mp4"

	vidHTML := renderString(t, galleryTileW(galleryImage{URL: vidURL, Width: 1024, Type: "video"}, "cm-meta-v", thumbnailWidth))
	if !strings.Contains(vidHTML, `data-video="1"`) {
		t.Errorf("video tile should carry data-video=\"1\"; html:\n%s", vidHTML)
	}
	if !strings.Contains(vidHTML, "cm-video-badge") || !strings.Contains(vidHTML, "▶") {
		t.Errorf("video tile should render the ▶ play badge; html:\n%s", vidHTML)
	}
	// data-full is the original (played as <video> in the lightbox).
	if !strings.Contains(vidHTML, `data-full="`+vidURL+`"`) {
		t.Errorf("video tile data-full should be the original url; html:\n%s", vidHTML)
	}
	// Poster thumbnail: civitaiThumbURL injects anim=false so a still frame shows.
	wantPoster := uuid + "/anim=false,width=450,optimized=true/clip.mp4"
	if !strings.Contains(vidHTML, `src="`+wantPoster+`"`) {
		t.Errorf("video tile src should be the anim=false poster; html:\n%s", vidHTML)
	}

	imgHTML := renderString(t, galleryTileW(galleryImage{URL: uuid + "/pic.jpeg", Width: 1024, Type: "image"}, "cm-meta-i", thumbnailWidth))
	if strings.Contains(imgHTML, "data-video") {
		t.Errorf("image tile must NOT carry data-video; html:\n%s", imgHTML)
	}
	if strings.Contains(imgHTML, "cm-video-badge") {
		t.Errorf("image tile must NOT render the ▶ badge; html:\n%s", imgHTML)
	}
}

// TestLightboxHasVideoElement proves the shared lightbox contains a <video> media
// node and that the page script wires the img/video toggle to it.
func TestLightboxHasVideoElement(t *testing.T) {
	box := renderString(t, lightboxOverlay())
	if !strings.Contains(box, `id="cm-lightbox-video"`) {
		t.Errorf("lightbox should contain a <video id=\"cm-lightbox-video\">; html:\n%s", box)
	}
	if !strings.Contains(box, "<video") {
		t.Errorf("lightbox should contain a <video> element; html:\n%s", box)
	}
	script := renderString(t, modelPageScript())
	if !strings.Contains(script, "cm-lightbox-video") {
		t.Error("modelPageScript should reference cm-lightbox-video (the video toggle)")
	}
	// The toggle must pause+clear the video on close (stops playback).
	if !strings.Contains(script, ".pause()") || !strings.Contains(script, "removeAttribute('src')") {
		t.Error("modelPageScript should pause + clear the video src on close")
	}
}

func TestHumanSince(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-2 * 24 * time.Hour), "2 days ago"},
		{"weeks", now.Add(-3 * 7 * 24 * time.Hour), "3 weeks ago"},
		{"months", now.Add(-2 * 30 * 24 * time.Hour), "2 months ago"},
		{"years", now.Add(-365 * 24 * time.Hour), "1 year ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := humanSince(c.t); got != c.want {
				t.Errorf("humanSince(%s): want %q, got %q", c.name, c.want, got)
			}
		})
	}
	if got := humanSince(time.Time{}); got != "never" {
		t.Errorf("zero time: want \"never\", got %q", got)
	}
	// Singular vs plural boundaries.
	if got := humanSince(now.Add(-25 * time.Hour)); got != "1 day ago" {
		t.Errorf("25h: want \"1 day ago\", got %q", got)
	}
}

func TestNewestVersionPublishedAt(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"modelVersions": []any{
			map[string]any{"id": 1, "publishedAt": "2023-01-01T00:00:00.000Z"},
			map[string]any{"id": 2, "publishedAt": "2024-06-15T12:30:00.000Z"}, // newest
			map[string]any{"id": 3, "publishedAt": "not-a-date"},               // skipped
			map[string]any{"id": 4}, // no field
		},
	})
	got := newestVersionPublishedAt(raw)
	want, _ := time.Parse(time.RFC3339, "2024-06-15T12:30:00.000Z")
	if !got.Equal(want) {
		t.Errorf("want newest %v, got %v", want, got)
	}
	// Empty / undecodable / all-unparseable → zero, no panic.
	if !newestVersionPublishedAt(nil).IsZero() {
		t.Error("nil raw should give zero time")
	}
	if !newestVersionPublishedAt([]byte("not json")).IsZero() {
		t.Error("garbage raw should give zero time")
	}
	allBad, _ := json.Marshal(map[string]any{"modelVersions": []any{
		map[string]any{"id": 1, "publishedAt": "nope"},
	}})
	if !newestVersionPublishedAt(allBad).IsZero() {
		t.Error("all-unparseable dates should give zero time")
	}
}

func TestNewestVersionInfoByModel(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"id": 10, "modelVersions": []any{
			map[string]any{"name": "v1", "publishedAt": "2022-01-01T00:00:00.000Z"},
			map[string]any{"name": "v3", "publishedAt": "2025-03-03T03:03:03.000Z"}, // newest for 10
		}},
		map[string]any{"id": 20, "modelVersions": []any{
			map[string]any{"name": "vX", "publishedAt": "bad"}, // no parseable date → absent
		}},
	}})
	got := newestVersionInfoByModel(raw)
	want, _ := time.Parse(time.RFC3339, "2025-03-03T03:03:03.000Z")
	if !got[10].At.Equal(want) {
		t.Errorf("model 10: want %v, got %v", want, got[10].At)
	}
	// The newest version's NAME is captured alongside its date.
	if got[10].Name != "v3" {
		t.Errorf("model 10: want newest version name v3, got %q", got[10].Name)
	}
	if _, ok := got[20]; ok {
		t.Error("model 20 has no parseable date and should be absent")
	}
	if got := newestVersionInfoByModel(nil); got == nil || len(got) != 0 {
		t.Errorf("nil raw should give a non-nil empty map, got %v", got)
	}
}

// headerCardView builds the modelDetailView modelHeaderCard now takes, from the
// loose args these tests used to pass positionally. verName/verDate are folded
// into the selected version (both empty → no selected version at all).
func headerCardView(m *civitai.ModelDetail, lastUpdated time.Time, selVID int, verName, verDate string) modelDetailView {
	v := modelDetailView{Model: m, LastUpdated: lastUpdated, SelectedVersionID: selVID}
	if verName != "" || verDate != "" {
		v.Version = &civitai.ModelVersionDetail{ID: selVID, Name: verName}
		v.PublishedAt = verDate
	}
	return v
}

// TestModelHeaderUpdatedStat proves the detail header renders "Updated X ago"
// with a hover popover (absolute date + latest version name/date) when a
// last-updated time is present, and omits it when zero.
func TestModelHeaderUpdatedStat(t *testing.T) {
	m := &civitai.ModelDetail{ID: 1, Name: "M", Type: "LORA"}

	set := renderString(t, modelHeaderCard(
		headerCardView(m, time.Now().Add(-3*time.Hour), 2, "v2", "2026-01-15"),
		nil, "csrf", "https://civitai.com"))
	if !strings.Contains(set, "Updated") || !strings.Contains(set, "3h ago") {
		t.Errorf("header should render \"Updated 3h ago\" when set; html:\n%s", set)
	}
	// The hover popover: the reusable class + the latest version name + publish date.
	for _, want := range []string{"cm-updated-pop", "Latest version: v2", "Published 2026-01-15"} {
		if !strings.Contains(set, want) {
			t.Errorf("header updated popover missing %q; html:\n%s", want, set)
		}
	}

	zero := renderString(t, modelHeaderCard(
		headerCardView(m, time.Time{}, 2, "v2", "2026-01-15"),
		nil, "csrf", "https://civitai.com"))
	if strings.Contains(zero, "Updated") {
		t.Errorf("header should omit \"Updated\" when the time is zero; html:\n%s", zero)
	}
}

// TestModelHeaderUpdatedDegradesAndEscapes proves the popover omits the version
// name/date lines when they are absent (no crash), and escapes a malicious version
// name (untrusted civitai string).
func TestModelHeaderUpdatedDegradesAndEscapes(t *testing.T) {
	m := &civitai.ModelDetail{ID: 1, Name: "M", Type: "LORA"}

	// No version name/date → the popover renders only the "Updated {date}" line.
	bare := renderString(t, modelHeaderCard(
		headerCardView(m, time.Now().Add(-1*time.Hour), 2, "", ""),
		nil, "csrf", "https://civitai.com"))
	if !strings.Contains(bare, "cm-updated-pop") {
		t.Error("popover should still render with only the date line")
	}
	if strings.Contains(bare, "Latest version:") || strings.Contains(bare, "Published ") {
		t.Errorf("absent version name/date lines should be omitted; html:\n%s", bare)
	}

	// A malicious version name must be escaped, not rendered as live markup.
	evil := renderString(t, modelHeaderCard(
		headerCardView(m, time.Now().Add(-1*time.Hour), 2, `<script>alert(1)</script>`, "2026-01-15"),
		nil, "csrf", "https://civitai.com"))
	if strings.Contains(evil, "<script>alert(1)</script>") {
		t.Errorf("version name must be escaped; html:\n%s", evil)
	}
	if !strings.Contains(evil, "&lt;script&gt;") {
		t.Errorf("version name should be HTML-escaped; html:\n%s", evil)
	}
}

// TestSearchCardUpdated proves a search result card renders "Updated X ago" with a
// hover popover (version name/date) for a model with a version publish date, and
// omits the whole thing when zero.
func TestSearchCardUpdated(t *testing.T) {
	it := civitai.ModelListItem{ID: 9, Name: "Vid Model", Type: "Checkpoint"}

	set := renderString(t, modelCard(it, nil, nil, fullMaturityRange(), "csrf",
		modelUpdateInfo{At: time.Now().Add(-2 * 24 * time.Hour), Name: "v9"}))
	if !strings.Contains(set, "Updated 2 days ago") {
		t.Errorf("search card should render \"Updated 2 days ago\"; html:\n%s", set)
	}
	for _, want := range []string{"cm-updated-pop", "Latest version: v9"} {
		if !strings.Contains(set, want) {
			t.Errorf("search card updated popover missing %q; html:\n%s", want, set)
		}
	}

	zero := renderString(t, modelCard(it, nil, nil, fullMaturityRange(), "csrf", modelUpdateInfo{}))
	if strings.Contains(zero, "Updated") {
		t.Errorf("search card should omit \"Updated\" when zero; html:\n%s", zero)
	}
}

// TestSearchCardUpdatedDeeplink proves the search card's "Latest version: {name}"
// popover line deeplinks to that version's in-app detail page
// (/models/{id}?version={vid}) when the version id is known.
func TestSearchCardUpdatedDeeplink(t *testing.T) {
	it := civitai.ModelListItem{ID: 9, Name: "Vid Model", Type: "Checkpoint"}
	out := renderString(t, modelCard(it, nil, nil, fullMaturityRange(), "csrf",
		modelUpdateInfo{At: time.Now().Add(-2 * 24 * time.Hour), Name: "v9", VersionID: 42}))
	if !strings.Contains(out, `href="/models/9?version=42"`) {
		t.Errorf("search card latest-version line should deeplink to the version page; html:\n%s", out)
	}
	if !strings.Contains(out, "Latest version: v9") {
		t.Errorf("latest-version link text should be present; html:\n%s", out)
	}
}

// TestUpdatedPopBodyPlainWithoutVersionID proves the popover renders the latest
// line as PLAIN TEXT (no link) when the version id is unknown (0), so a missing id
// never produces a broken /models/{id}?version=0 link.
func TestUpdatedPopBodyPlainWithoutVersionID(t *testing.T) {
	out := renderString(t, updatedPopBody(9, 0, "2026-01-15 10:00", "v9", "2026-01-15"))
	if strings.Contains(out, "href=") {
		t.Errorf("no version id → the latest line must not be a link; html:\n%s", out)
	}
	if !strings.Contains(out, "Latest version: v9") {
		t.Errorf("latest-version line should still render as text; html:\n%s", out)
	}
}

// TestSearchCardUpdatedEscapesVersionName proves a malicious version name threaded
// into a search card's updated popover is escaped.
func TestSearchCardUpdatedEscapesVersionName(t *testing.T) {
	it := civitai.ModelListItem{ID: 9, Name: "Vid Model", Type: "Checkpoint"}
	out := renderString(t, modelCard(it, nil, nil, fullMaturityRange(), "csrf",
		modelUpdateInfo{At: time.Now().Add(-1 * time.Hour), Name: `<img src=x onerror=alert(1)>`}))
	if strings.Contains(out, "<img src=x onerror=alert(1)>") {
		t.Errorf("version name must be escaped in the search card popover; html:\n%s", out)
	}
	if strings.Contains(out, "onerror=alert(1)") && !strings.Contains(out, "&lt;img") {
		t.Errorf("version name should be HTML-escaped; html:\n%s", out)
	}
}
