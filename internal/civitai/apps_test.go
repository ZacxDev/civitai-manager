package civitai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// appsBody is a synthetic /api/v1/apps response with one offsite and one onsite
// item plus a nextCursor, mirroring the ListingCard contract. (The live catalog
// is dark today, so a captured/synthetic body is the only way to exercise the
// populated decode path.)
const appsBody = `{
  "items": [
    {
      "id": 1, "slug": "cool-offsite", "kind": "offsite",
      "name": "Cool Offsite App", "tagline": "does cool things",
      "category": "utility", "contentRating": "PG",
      "iconUrl": "https://cdn/icon1.png", "coverUrl": "https://cdn/cover1.png",
      "creator": {"id": 10, "username": "alice", "image": "https://cdn/alice.png"},
      "recommend": {"recommendedCount": 8, "notRecommendedCount": 2, "recommendPct": 80},
      "reviewCount": 10,
      "kindData": {"subKind": "web", "externalUrl": "https://example.com/app"}
    },
    {
      "id": 2, "slug": "neat-onsite", "kind": "onsite",
      "name": "Neat Onsite App", "tagline": "runs on civitai",
      "category": "image", "contentRating": "PG",
      "iconUrl": "https://cdn/icon2.png", "coverUrl": "",
      "creator": {"id": 11, "username": "bob"},
      "recommend": {"recommendedCount": 0, "notRecommendedCount": 0, "recommendPct": null},
      "reviewCount": 0,
      "kindData": {"appBlockId": 99, "hasPage": true, "liveUrl": "https://neat.civitai.com"}
    }
  ],
  "metadata": {"nextCursor": "CURSOR2", "nextPage": "https://civitai.com/api/v1/apps?cursor=CURSOR2"}
}`

func TestListAppsDecodesItemsAndMetadata(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(appsBody))
	}))
	defer srv.Close()

	c := NewAppsClient(srv.URL, "tok")
	page, err := c.ListApps(context.Background(), AppsParams{
		Kind: "all", Category: "utility", Sort: "newest", Cursor: "C1", Limit: 24,
	})
	if err != nil {
		t.Fatalf("ListApps error: %v", err)
	}

	// Request shape: params forwarded, token sent as Bearer.
	u, _ := url.Parse(gotPath)
	q := u.Query()
	for k, want := range map[string]string{
		"kind": "all", "category": "utility", "sort": "newest", "cursor": "C1", "limit": "24",
	} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if u.Path != "/api/v1/apps" {
		t.Errorf("path = %q, want /api/v1/apps", u.Path)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}

	// Decode: two items, offsite + onsite fields populated correctly.
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	off := page.Items[0]
	if off.Kind != "offsite" || off.Name != "Cool Offsite App" || off.KindData.ExternalURL != "https://example.com/app" {
		t.Errorf("offsite item decoded wrong: %+v", off)
	}
	if off.Recommend.RecommendPct == nil || *off.Recommend.RecommendPct != 80 {
		t.Errorf("offsite recommendPct = %v, want 80", off.Recommend.RecommendPct)
	}
	if off.Creator.Username != "alice" {
		t.Errorf("offsite creator = %q, want alice", off.Creator.Username)
	}
	on := page.Items[1]
	if on.Kind != "onsite" || on.KindData.LiveURL != "https://neat.civitai.com" {
		t.Errorf("onsite item decoded wrong: %+v", on)
	}
	// Nullable recommendPct decodes to nil without breaking the whole decode.
	if on.Recommend.RecommendPct != nil {
		t.Errorf("onsite recommendPct = %v, want nil", on.Recommend.RecommendPct)
	}
	if page.Metadata.NextCursor != "CURSOR2" {
		t.Errorf("nextCursor = %q, want CURSOR2", page.Metadata.NextCursor)
	}
}

func TestListAppsEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"metadata":{}}`))
	}))
	defer srv.Close()

	page, err := NewAppsClient(srv.URL, "").ListApps(context.Background(), AppsParams{})
	if err != nil {
		t.Fatalf("empty catalog should NOT error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("items = %d, want 0", len(page.Items))
	}
	if page.Metadata.NextCursor != "" {
		t.Errorf("nextCursor = %q, want empty", page.Metadata.NextCursor)
	}
}

func TestListAppsNoTokenOmitsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[],"metadata":{}}`))
	}))
	defer srv.Close()

	if _, err := NewAppsClient(srv.URL, "").ListApps(context.Background(), AppsParams{}); err != nil {
		t.Fatalf("ListApps error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("anonymous browse must not send Authorization, got %q", gotAuth)
	}
}

func TestListAppsNon2xxTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	_, err := NewAppsClient(srv.URL, "").ListApps(context.Background(), AppsParams{})
	if err == nil {
		t.Fatal("non-2xx should return an error")
	}
	var ae *AppsError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AppsError, got %T: %v", err, err)
	}
	if ae.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", ae.StatusCode)
	}
}
