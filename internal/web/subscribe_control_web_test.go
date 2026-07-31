package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestSubscribeConfirmFailureShowsError proves a genuine subscribe failure
// surfaces an error note in the control instead of silently collapsing to a bare
// Subscribe button — the "feedback" the UX promises.
func TestSubscribeConfirmFailureShowsError(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, stubReader{}, storeSubscriber{st: st, err: errors.New("model gone")},
		Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787"}, nil)

	rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Subscribe failed") {
		t.Errorf("a failed subscribe should surface an error note; got:\n%s", out)
	}
	if strings.Contains(out, "/models/7/unsubscribe") {
		t.Error("a failed subscribe must not render the subscribed/unsubscribe state")
	}
	if sub := srv.modelSubscription(7); sub != nil {
		t.Errorf("no subscription should be persisted after a failed subscribe, got %+v", sub)
	}
}

// TestSubscribeOptionsPanel proves the options panel (GET /models/{id}/subscribe-options)
// renders the auto-download (default) vs notify-only choice, a Confirm posting the
// subscribe endpoint with CSRF, and a Cancel returning to the collapsed control.
func TestSubscribeOptionsPanel(t *testing.T) {
	srv := newSubscribeServer(t)
	// Seed a cached name so the heading resolves it (zero civitai calls).
	if err := srv.store.PutModelCache(7, "Nice Model", []byte(`{"name":"Nice Model"}`)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	rec := get(t, srv, "/models/7/subscribe-options")
	if rec.Code != http.StatusOK {
		t.Fatalf("options = %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{
		`id="subscribe-control-7"`,             // stable container
		"Subscribe to Nice Model?",             // heading uses the cached name
		`type="radio"`,                         // the mode choice
		`value="auto_download"`,                // auto-download option
		`value="notify_only"`,                  // notify-only option
		`hx-post="/models/7/subscribe"`,        // Confirm posts subscribe
		`hx-get="/models/7/subscribe-control"`, // Cancel returns collapsed
		"Confirm",
		"Cancel",
		srv.csrf, // CSRF token carried in the form
	} {
		if !strings.Contains(out, want) {
			t.Errorf("options panel missing %q\n%s", want, out)
		}
	}
	// Auto-download is the DEFAULT (checked) radio; notify-only is not.
	if !strings.Contains(out, `value="auto_download" class="text-indigo-500" checked`) {
		t.Errorf("auto-download should be the default-checked radio:\n%s", out)
	}
	if strings.Contains(out, `value="notify_only" class="text-indigo-500" checked`) {
		t.Error("notify-only should NOT be pre-checked")
	}
}

// TestSubscribeOptionsPanelUnknownName falls back to "this model" when there is
// no cached name (zero civitai calls).
func TestSubscribeOptionsPanelUnknownName(t *testing.T) {
	srv := newSubscribeServer(t)
	rec := get(t, srv, "/models/99/subscribe-options")
	if rec.Code != http.StatusOK {
		t.Fatalf("options = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Subscribe to this model?") {
		t.Errorf("options heading should fall back to 'this model':\n%s", rec.Body.String())
	}
}

// TestSubscribeConfirmAutoDownload proves Confirm with auto-download creates an
// auto-download sub and returns the "Subscribed ✓" feedback with an Unsubscribe.
func TestSubscribeConfirmAutoDownload(t *testing.T) {
	srv := newSubscribeServer(t)
	rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Subscribed ✓") || !strings.Contains(out, "auto-download") {
		t.Errorf("feedback should confirm an auto-download subscription:\n%s", out)
	}
	if !strings.Contains(out, `hx-post="/models/7/unsubscribe"`) {
		t.Error("subscribed feedback should offer Unsubscribe")
	}
	sub := srv.modelSubscription(7)
	if sub == nil || !sub.AutoDownload || sub.NotifyOnly {
		t.Fatalf("want an auto-download sub, got %+v", sub)
	}
}

// TestSubscribeConfirmNotifyOnly proves Confirm with notify-only creates a
// notify-only sub and reflects that mode in the feedback.
func TestSubscribeConfirmNotifyOnly(t *testing.T) {
	srv := newSubscribeServer(t)
	rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"notify_only"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "notify only") {
		t.Errorf("feedback should reflect notify-only mode:\n%s", rec.Body.String())
	}
	sub := srv.modelSubscription(7)
	if sub == nil || !sub.NotifyOnly || sub.AutoDownload {
		t.Fatalf("want a notify-only sub, got %+v", sub)
	}
}

// TestSubscribeCancelReturnsCollapsed proves the Cancel endpoint re-renders the
// collapsed Subscribe control (not the options panel) when not subscribed.
func TestSubscribeCancelReturnsCollapsed(t *testing.T) {
	srv := newSubscribeServer(t)
	rec := get(t, srv, "/models/7/subscribe-control")
	if rec.Code != http.StatusOK {
		t.Fatalf("control = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `hx-get="/models/7/subscribe-options"`) {
		t.Error("collapsed control should open the options panel")
	}
	if strings.Contains(out, `type="radio"`) {
		t.Error("collapsed control should NOT be the options panel")
	}
}

// TestSubscribeCSRFRejectedBeforeMutation proves a missing/bad CSRF token is
// rejected (403) and creates no subscription.
func TestSubscribeCSRFRejectedBeforeMutation(t *testing.T) {
	// Missing token → 403, no mutation.
	t.Run("missing token", func(t *testing.T) {
		srv := newSubscribeServer(t)
		rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403 without CSRF, got %d", rec.Code)
		}
		if srv.modelSubscription(7) != nil {
			t.Error("no subscription should be created when CSRF is rejected")
		}
	})
	// Wrong token → 403, no mutation.
	t.Run("wrong token", func(t *testing.T) {
		srv := newSubscribeServer(t)
		req := httptest.NewRequest(http.MethodPost, "/models/7/subscribe",
			strings.NewReader(url.Values{"mode": {"auto_download"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-CSRF-Token", "not-the-token")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403 with wrong CSRF, got %d", rec.Code)
		}
		if srv.modelSubscription(7) != nil {
			t.Error("no subscription should be created when CSRF is wrong")
		}
	})
	// Unsubscribe is likewise CSRF-guarded.
	t.Run("unsubscribe missing token", func(t *testing.T) {
		srv := newSubscribeServer(t)
		if rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, true); rec.Code != http.StatusOK {
			t.Fatalf("subscribe = %d", rec.Code)
		}
		rec := post(t, srv, "/models/7/unsubscribe", url.Values{}, false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403 unsubscribe without CSRF, got %d", rec.Code)
		}
		if srv.modelSubscription(7) == nil {
			t.Error("subscription must survive a CSRF-rejected unsubscribe")
		}
	})
}

// TestCreatorSubscribeShowsSuccessNote proves the creator subscribe stays
// one-click (POST /subscribe, auto-download pre-selected) and carries a success
// note revealed after a successful request.
//
// The auto-download choice used to be a HIDDEN `auto_download=true` input with no
// options at all, which made "Notify only" unreachable on the creator path — see
// TestCreatorSubscribeOffersNotifyOnly, which pins the replacement. The default is
// still auto-download, so the one-click behaviour this test was written for is
// unchanged.
func TestCreatorSubscribeShowsSuccessNote(t *testing.T) {
	out := renderString(t, subscribeInline("creator", "alice", "Subscribe to creator", "test-csrf"))
	for _, want := range []string{
		`hx-post="/subscribe"`, // still the one-click dashboard subscribe
		`name="creator"`,       // creator field
		`value="alice"`,        // the target
		`value="auto_download" class="text-indigo-500" checked`, // auto-download still the default
		"hx-on::after-request", // reveals the note on success
		"data-sub-note",        // the note element
		"Subscribed ✓",         // the success message
		"Subscribe to creator", // the button label
	} {
		if !strings.Contains(out, want) {
			t.Errorf("creator subscribe missing %q\n%s", want, out)
		}
	}
	// The hidden input is GONE — if it came back it would silently override the
	// radio (checkboxVal reads auto_download directly) and re-break Notify only.
	if strings.Contains(out, `type="hidden" name="auto_download"`) {
		t.Errorf("the hardcoded hidden auto_download input must not come back:\n%s", out)
	}
}

// TestSubscribeIdempotent proves subscribing an already-subscribed model does not
// duplicate and still returns the subscribed feedback (ErrAlreadySubscribed).
func TestSubscribeIdempotent(t *testing.T) {
	srv := newSubscribeServer(t)
	if rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("first subscribe = %d", rec.Code)
	}
	rec := post(t, srv, "/models/7/subscribe", url.Values{"mode": {"auto_download"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("second subscribe = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Subscribed ✓") {
		t.Errorf("re-subscribe should still return the subscribed feedback:\n%s", rec.Body.String())
	}
	if sub := srv.modelSubscription(7); sub == nil {
		t.Fatal("subscription should still exist after idempotent re-subscribe")
	}
}
