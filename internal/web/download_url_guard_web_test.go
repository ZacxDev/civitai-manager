package web

import (
	"context"
	"testing"
)

// TestAssertHTTPSDownloadURL is the app-level https-only belt on API-supplied
// download URLs (security audit v0.1.64, 🟡-2). https passes; every other scheme
// (http, file, ftp, empty) is refused.
func TestAssertHTTPSDownloadURL(t *testing.T) {
	cases := []struct {
		url    string
		wantOK bool
	}{
		{"https://civitai.com/api/download/models/1759168", true}, // real CivitAI shape (live-verified)
		{"https://huggingface.co/x/resolve/main/model.safetensors", true},
		{"HTTPS://civitai.com/x", true}, // scheme compare is case-insensitive
		{"http://192.168.50.10/evil", false},
		{"http://civitai.com/x", false},
		{"file:///etc/passwd", false},
		{"ftp://civitai.com/x", false},
		{"://nonsense", false},
		{"", false},
	}
	for _, c := range cases {
		err := assertHTTPSDownloadURL(c.url)
		if c.wantOK && err != nil {
			t.Errorf("assertHTTPSDownloadURL(%q) = %v; want pass", c.url, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("assertHTTPSDownloadURL(%q) = nil; want refusal", c.url)
		}
	}
}

// TestFetchBoundedRefusesNonHTTPSBeforeSeam proves the guard fires BEFORE the
// download seam: a non-https URL is refused and DownloadFile is never called; a
// real-shaped https URL reaches the (fake) downloader.
func TestFetchBoundedRefusesNonHTTPSBeforeSeam(t *testing.T) {
	dl := &fakeDownloader{zips: map[string][]byte{
		"https://civitai.com/api/download/models/1": []byte("ok"),
	}}

	if _, err := fetchBounded(context.Background(), dl, "http://192.168.50.10/evil.zip"); err == nil {
		t.Fatal("expected refusal for http:// URL")
	}
	if dl.calls != 0 {
		t.Fatalf("DownloadFile was called %d times for an http:// URL; must be 0 (no egress)", dl.calls)
	}

	if _, err := fetchBounded(context.Background(), dl, "file:///etc/passwd"); err == nil {
		t.Fatal("expected refusal for file:// URL")
	}
	if dl.calls != 0 {
		t.Fatalf("DownloadFile was called %d times for a file:// URL; must be 0", dl.calls)
	}

	// https reaches the seam.
	if _, err := fetchBounded(context.Background(), dl, "https://civitai.com/api/download/models/1"); err != nil {
		t.Fatalf("https URL should reach the downloader: %v", err)
	}
	if dl.calls != 1 {
		t.Fatalf("DownloadFile calls = %d; want 1 for the https URL", dl.calls)
	}
}
