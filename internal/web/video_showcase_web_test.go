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

	vidHTML := renderString(t, galleryTile(galleryImage{URL: vidURL, Width: 1024, Type: "video"}, "cm-meta-v", false))
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

	imgHTML := renderString(t, galleryTile(galleryImage{URL: uuid + "/pic.jpeg", Width: 1024, Type: "image"}, "cm-meta-i", false))
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

func TestNewestPublishedAtByModel(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"id": 10, "modelVersions": []any{
			map[string]any{"publishedAt": "2022-01-01T00:00:00.000Z"},
			map[string]any{"publishedAt": "2025-03-03T03:03:03.000Z"}, // newest for 10
		}},
		map[string]any{"id": 20, "modelVersions": []any{
			map[string]any{"publishedAt": "bad"}, // no parseable date → absent
		}},
	}})
	got := newestPublishedAtByModel(raw)
	want, _ := time.Parse(time.RFC3339, "2025-03-03T03:03:03.000Z")
	if !got[10].Equal(want) {
		t.Errorf("model 10: want %v, got %v", want, got[10])
	}
	if _, ok := got[20]; ok {
		t.Error("model 20 has no parseable date and should be absent")
	}
	if got := newestPublishedAtByModel(nil); got == nil || len(got) != 0 {
		t.Errorf("nil raw should give a non-nil empty map, got %v", got)
	}
}

// TestModelHeaderUpdatedStat proves the detail header renders "Updated X ago"
// only when a last-updated time is present, and omits it when zero.
func TestModelHeaderUpdatedStat(t *testing.T) {
	m := &civitai.ModelDetail{ID: 1, Name: "M", Type: "LORA"}

	set := renderString(t, modelHeaderCard(m, "", "csrf", "https://civitai.com/models/1", nil, time.Now().Add(-3*time.Hour)))
	if !strings.Contains(set, "Updated") || !strings.Contains(set, "3h ago") {
		t.Errorf("header should render \"Updated 3h ago\" when set; html:\n%s", set)
	}

	zero := renderString(t, modelHeaderCard(m, "", "csrf", "https://civitai.com/models/1", nil, time.Time{}))
	if strings.Contains(zero, "Updated") {
		t.Errorf("header should omit \"Updated\" when the time is zero; html:\n%s", zero)
	}
}

// TestSearchCardUpdated proves a search result card renders "Updated X ago" for a
// model with a version publish date, and omits it when zero.
func TestSearchCardUpdated(t *testing.T) {
	it := civitai.ModelListItem{ID: 9, Name: "Vid Model", Type: "Checkpoint"}

	set := renderString(t, modelCard(it, nil, nil, NSFWShow, "csrf", time.Now().Add(-2*24*time.Hour)))
	if !strings.Contains(set, "Updated 2 days ago") {
		t.Errorf("search card should render \"Updated 2 days ago\"; html:\n%s", set)
	}

	zero := renderString(t, modelCard(it, nil, nil, NSFWShow, "csrf", time.Time{}))
	if strings.Contains(zero, "Updated") {
		t.Errorf("search card should omit \"Updated\" when zero; html:\n%s", zero)
	}
}
