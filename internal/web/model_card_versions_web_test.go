package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// threeVersions is a model's version list, civitai NEWEST-FIRST: v3 is the latest.
func threeVersions() []civitai.ModelVersionSummary {
	return []civitai.ModelVersionSummary{
		{ID: 30, Name: "v3", BaseModel: "SDXL"},
		{ID: 20, Name: "v2", BaseModel: "SDXL"},
		{ID: 10, Name: "v1", BaseModel: "SD 1.5"},
	}
}

func localFile(vid int, bytes int64) store.LocalFile {
	return store.LocalFile{
		ModelID: intPtr(7), VersionID: intPtr(vid), SizeBytes: bytes,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
	}
}

// TestBuildVersionBreakdown exercises the pure grouping + newer-detection helper
// across the required cases: user has latest (no update), user has an older
// version (update available), user has none locally (update available).
func TestBuildVersionBreakdown(t *testing.T) {
	versions := threeVersions()

	t.Run("has latest → no update", func(t *testing.T) {
		b := buildVersionBreakdown(versions, []store.LocalFile{localFile(30, 100), localFile(30, 200)})
		if b.UpdateAvailable {
			t.Error("owning the latest version must NOT report an update")
		}
		if len(b.Local) != 1 || b.Local[0].VersionID != 30 || b.Local[0].Name != "v3" {
			t.Fatalf("local groups = %+v, want one v3 group", b.Local)
		}
		if b.Local[0].Bytes != 300 || b.Local[0].FileCount != 2 {
			t.Errorf("v3 group size=%d count=%d, want 300/2", b.Local[0].Bytes, b.Local[0].FileCount)
		}
		if b.TotalBytes != 300 {
			t.Errorf("total bytes = %d, want 300", b.TotalBytes)
		}
		// The latest is in-library; nothing is "newer".
		for _, av := range b.Available {
			if av.ID == 30 && !av.InLibrary {
				t.Error("latest should be marked in library")
			}
			if av.Newer {
				t.Errorf("no version should be newer when you own the latest: %+v", av)
			}
		}
	})

	t.Run("has older → update available", func(t *testing.T) {
		b := buildVersionBreakdown(versions, []store.LocalFile{localFile(10, 50)})
		if !b.UpdateAvailable {
			t.Error("owning only an older version must report an update")
		}
		if b.LatestID != 30 || b.LatestName != "v3" {
			t.Errorf("latest = %d/%q, want 30/v3", b.LatestID, b.LatestName)
		}
		byID := map[int]availableVersion{}
		for _, av := range b.Available {
			byID[av.ID] = av
		}
		if !byID[30].Newer || !byID[20].Newer {
			t.Errorf("v3 and v2 should both be newer than the user's v1: %+v", b.Available)
		}
		if !byID[10].InLibrary || byID[10].Newer {
			t.Errorf("owned v1 should be in-library and not newer: %+v", byID[10])
		}
	})

	t.Run("has none → update available, no local groups", func(t *testing.T) {
		b := buildVersionBreakdown(versions, nil)
		if !b.UpdateAvailable {
			t.Error("owning nothing must report an update")
		}
		if len(b.Local) != 0 {
			t.Errorf("no local files → no local groups, got %+v", b.Local)
		}
	})

	t.Run("unknown local version id falls back to Version #id", func(t *testing.T) {
		b := buildVersionBreakdown(versions, []store.LocalFile{localFile(999, 10)})
		if len(b.Local) != 1 || b.Local[0].Name != "Version #999" {
			t.Fatalf("unknown version should fall back to Version #999, got %+v", b.Local)
		}
	})
}

// TestVersionBreakdownRendersSection proves the card renders the expandable
// version breakdown: an update-available banner linking to the in-app model page
// at the latest version, the two <details> panels, and the in-library marker.
func TestVersionBreakdownRendersSection(t *testing.T) {
	m := &civitai.ModelDetail{ID: 7, Name: "Great Model", ModelVersions: threeVersions()}
	// User owns only v1 → update to v3 is available.
	view := buildMatchedModelCardView(7, m, nil, []store.LocalFile{localFile(10, 50)}, NSFWBlur, nil)
	out := renderString(t, matchedModelCard(view, "csrf"))

	for _, want := range []string{
		"Update available: v3 →",       // prominent indicator
		`href="/models/7?version=30"`,  // links to the in-app page at the latest version
		"Versions in your library (1)", // expandable local-versions details
		"<details", "<summary",         // the expandable containers
		"Available versions (3)",                 // expandable available-versions details
		"in library",                             // owned-version marker
		"newer →", `href="/models/7?version=20"`, // a newer available version is linked
	} {
		if !strings.Contains(out, want) {
			t.Errorf("version breakdown missing %q:\n%s", want, out)
		}
	}
}

// storeSubscriber is a Subscriber that actually writes a model subscription into
// the store, so the subscribe handler's re-render reflects real persisted state.
type storeSubscriber struct{ st *store.Store }

func (s storeSubscriber) SubscribeModel(_ context.Context, modelID int, opts poller.SubscribeOptions) (int64, error) {
	if _, err := s.st.FindModelSubscription(modelID); err == nil {
		return 0, poller.ErrAlreadySubscribed
	}
	return s.st.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &modelID,
		AutoDownload: opts.AutoDownload, NotifyOnly: opts.NotifyOnly, PollIntervalSecs: 3600,
	})
}
func (s storeSubscriber) SubscribeCreator(context.Context, string, poller.SubscribeOptions) (int64, error) {
	return 0, nil
}

func newSubscribeServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, storeSubscriber{st}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787",
	}, nil)
}

// TestModelSubscriptionLookup proves modelSubscription resolves via the indexed
// FindModelSubscription: nil when there is no subscription (the ErrNotFound path)
// and the persisted subscription when one exists, unaffected by an unrelated
// creator subscription (no model id) also present in the table.
func TestModelSubscriptionLookup(t *testing.T) {
	srv := newSubscribeServer(t)

	// No subscription yet → ErrNotFound → nil (never an error/log).
	if got := srv.modelSubscription(7); got != nil {
		t.Fatalf("unsubscribed model should resolve nil, got %+v", got)
	}

	// A creator subscription (model_id NULL) must not be mistaken for a model sub.
	if _, err := srv.store.CreateSubscription(store.Subscription{
		Kind: store.KindCreator, Username: "someone", PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if got := srv.modelSubscription(7); got != nil {
		t.Fatalf("creator sub must not resolve as model 7 sub, got %+v", got)
	}

	// Subscribe model 7 → the lookup returns exactly that subscription.
	mid := 7
	if _, err := srv.store.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	got := srv.modelSubscription(7)
	if got == nil || got.Kind != store.KindModel || got.ModelID == nil || *got.ModelID != 7 {
		t.Fatalf("subscribed model 7 should resolve its sub, got %+v", got)
	}
	// A different model id is still unsubscribed.
	if other := srv.modelSubscription(8); other != nil {
		t.Fatalf("model 8 has no sub, got %+v", other)
	}
}

// TestModelSubscribeToggleHandlers proves the matched-card subscribe/unsubscribe
// endpoints create/delete an auto-download model subscription and return the
// updated toggle fragment, and both reject a missing/bad CSRF token.
func TestModelSubscribeToggleHandlers(t *testing.T) {
	srv := newSubscribeServer(t)

	// Not subscribed yet → the toggle offers Subscribe.
	if got := renderString(t, subscribeToggle(7, srv.modelSubscription(7), srv.csrf)); !strings.Contains(got, "Subscribe") || strings.Contains(got, "Unsubscribe") {
		t.Fatalf("initial toggle should offer Subscribe, got:\n%s", got)
	}

	// Subscribe: creates an auto-download sub and returns the SUBSCRIBED toggle.
	rec := post(t, srv, "/models/7/subscribe", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Unsubscribe") {
		t.Errorf("subscribe response should return the subscribed toggle:\n%s", rec.Body.String())
	}
	sub := srv.modelSubscription(7)
	if sub == nil || !sub.AutoDownload {
		t.Fatalf("subscribe must create an auto-download subscription, got %+v", sub)
	}
	if !strings.Contains(rec.Body.String(), `hx-post="/models/7/unsubscribe"`) {
		t.Errorf("subscribed toggle should post to unsubscribe:\n%s", rec.Body.String())
	}

	// Unsubscribe: deletes it and returns the collapsed subscribe control (which
	// opens the options panel) with an "Unsubscribed" note.
	rec = post(t, srv, "/models/7/unsubscribe", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe = %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `hx-post="/models/7/unsubscribe"`) {
		t.Errorf("unsubscribe response should return the collapsed subscribe control:\n%s", body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `hx-get="/models/7/subscribe-options"`) || !strings.Contains(body, "Unsubscribed") {
		t.Errorf("unsubscribe response should show the collapsed control + Unsubscribed note:\n%s", body)
	}
	if srv.modelSubscription(7) != nil {
		t.Error("unsubscribe must delete the subscription")
	}

	// CSRF is required on both endpoints.
	for _, path := range []string{"/models/7/subscribe", "/models/7/unsubscribe"} {
		if rec := post(t, srv, path, url.Values{}, false); rec.Code != http.StatusForbidden {
			t.Errorf("%s without CSRF = %d, want 403", path, rec.Code)
		}
	}
}
