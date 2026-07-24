package web

import (
	"strings"
	"testing"
)

func TestCivitaiThumbURL(t *testing.T) {
	const uuid = "https://image.civitai.com/xG1nkqKTMzGDvpLrqFT7WA/ad0eb2e0-c228-4131-956d-ca01b95552d3"
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{
			name:  "insert params when none present",
			in:    uuid + "/Krea2upscale_00942_.jpeg",
			width: 450,
			want:  uuid + "/anim=false,width=450,optimized=true/Krea2upscale_00942_.jpeg",
		},
		{
			name:  "replace existing transform segment",
			in:    uuid + "/width=1024/Krea2upscale_00942_.jpeg",
			width: 450,
			want:  uuid + "/anim=false,width=450,optimized=true/Krea2upscale_00942_.jpeg",
		},
		{
			name:  "replace existing multi-param transform segment",
			in:    uuid + "/anim=false,width=1200,optimized=true/x.jpeg",
			width: 450,
			want:  uuid + "/anim=false,width=450,optimized=true/x.jpeg",
		},
		{
			name:  "non-civitai host untouched",
			in:    "https://example.com/a/b/c.jpeg",
			width: 450,
			want:  "https://example.com/a/b/c.jpeg",
		},
		{
			name:  "width<=0 untouched",
			in:    uuid + "/x.jpeg",
			width: 0,
			want:  uuid + "/x.jpeg",
		},
		{
			name:  "unexpected short path untouched",
			in:    "https://image.civitai.com/onlyone",
			width: 450,
			want:  "https://image.civitai.com/onlyone",
		},
		{
			name:  "unparseable untouched",
			in:    "://not a url",
			width: 450,
			want:  "://not a url",
		},
		{
			name:  "empty untouched",
			in:    "",
			width: 450,
			want:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := civitaiThumbURL(c.in, c.width)
			if got != c.want {
				t.Fatalf("civitaiThumbURL(%q,%d)\n got: %q\nwant: %q", c.in, c.width, got, c.want)
			}
			// The params segment must never be percent-encoded (the CDN needs the
			// literal commas/equals).
			if strings.Contains(got, "%2C") || strings.Contains(got, "%3D") {
				t.Fatalf("params segment was percent-encoded: %q", got)
			}
		})
	}
}

func TestTileThumbWidthNoUpscale(t *testing.T) {
	if w := tileThumbWidth(galleryImage{Width: 0}); w != thumbnailWidth {
		t.Fatalf("unknown width should request %d, got %d", thumbnailWidth, w)
	}
	if w := tileThumbWidth(galleryImage{Width: 4096}); w != thumbnailWidth {
		t.Fatalf("large image should be downscaled to %d, got %d", thumbnailWidth, w)
	}
	if w := tileThumbWidth(galleryImage{Width: 200}); w != 200 {
		t.Fatalf("small original should not be upscaled; want 200 got %d", w)
	}
}

// TestGalleryTileUsesThumbForSrcOriginalForLightbox proves the grid tile src is
// the downscaled thumbnail while the click-to-zoom lightbox (data-full) keeps the
// full-resolution original.
func TestGalleryTileUsesThumbForSrcOriginalForLightbox(t *testing.T) {
	orig := "https://image.civitai.com/xG1nkqKTMzGDvpLrqFT7WA/ad0eb2e0-c228-4131-956d-ca01b95552d3/Krea2upscale_00942_.jpeg"
	html := renderString(t, galleryTile(galleryImage{URL: orig, Width: 1024}, "cm-meta-0", false))

	wantThumb := "https://image.civitai.com/xG1nkqKTMzGDvpLrqFT7WA/ad0eb2e0-c228-4131-956d-ca01b95552d3/anim=false,width=450,optimized=true/Krea2upscale_00942_.jpeg"
	if !strings.Contains(html, `src="`+wantThumb+`"`) {
		t.Fatalf("tile src should be the width=450 thumbnail; html:\n%s", html)
	}
	if !strings.Contains(html, `data-full="`+orig+`"`) {
		t.Fatalf("data-full should be the original full-res URL; html:\n%s", html)
	}
}
