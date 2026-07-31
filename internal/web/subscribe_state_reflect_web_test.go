package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// cannedSearchReader is a civitai.Reader (via the stubReader base) whose
// SearchModels returns a fixed set of items, so the search/creator handlers can be
// exercised with deterministic result cards.
type cannedSearchReader struct {
	stubReader
	items []civitai.ModelListItem
}

func (r cannedSearchReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{Items: r.items}, nil
}

// subReflectServer builds a server backed by a real store (persisted subs) and a
// canned search reader returning items.
func subReflectServer(t *testing.T, items []civitai.ModelListItem) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, cannedSearchReader{items: items}, storeSubscriber{st: st}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787",
	}, nil)
}

// seedModelSub persists an auto-download model subscription for the given id.
func seedModelSub(t *testing.T, st *store.Store, modelID int) {
	t.Helper()
	mid := modelID
	if _, err := st.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatalf("seed sub %d: %v", modelID, err)
	}
}

// TestModelPageReflectsSubscribedState proves the model detail header renders the
// SUBSCRIBED control (Unsubscribe) when the model is subscribed, and the collapsed
// Subscribe control when it is not — instead of always showing "Subscribe".
func TestModelPageReflectsSubscribedState(t *testing.T) {
	// Not subscribed → collapsed Subscribe (opens the options panel), no Unsubscribe.
	t.Run("not subscribed", func(t *testing.T) {
		srv := newSubscribeServer(t) // stubReader.GetModel → model id 1
		rec := get(t, srv, "/models/1")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /models/1 = %d", rec.Code)
		}
		out := rec.Body.String()
		if !strings.Contains(out, `hx-get="/models/1/subscribe-options"`) {
			t.Errorf("unsubscribed model page should render the collapsed Subscribe control:\n%s", out)
		}
		if strings.Contains(out, `hx-post="/models/1/unsubscribe"`) {
			t.Error("unsubscribed model page must not render the Unsubscribe control")
		}
	})

	// Subscribed → the Subscribed ✓ / Unsubscribe control.
	t.Run("subscribed", func(t *testing.T) {
		srv := newSubscribeServer(t)
		seedModelSub(t, srv.store, 1)
		rec := get(t, srv, "/models/1")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /models/1 = %d", rec.Code)
		}
		out := rec.Body.String()
		if !strings.Contains(out, `hx-post="/models/1/unsubscribe"`) {
			t.Errorf("subscribed model page should render the Unsubscribe control:\n%s", out)
		}
		if !strings.Contains(out, "Subscribed ✓") {
			t.Errorf("subscribed model page should show the Subscribed ✓ feedback:\n%s", out)
		}
	})
}

// TestModelDetailPageRendersSubControl is the render-level twin: modelDetailPage
// with a non-nil sub renders Unsubscribe; with nil it renders collapsed Subscribe.
func TestModelDetailPageRendersSubControl(t *testing.T) {
	m := &civitai.ModelDetail{ID: 42, Name: "M", Type: "LORA",
		ModelVersions: []civitai.ModelVersionSummary{{ID: 1, Name: "v1"}}}
	view := modelDetailView{Model: m, SelectedVersionID: 1,
		Version: &civitai.ModelVersionDetail{ID: 1}}

	nilOut := renderString(t, modelDetailPage(view, nil, "csrf", "dark", "https://civitai.com"))
	if !strings.Contains(nilOut, `hx-get="/models/42/subscribe-options"`) {
		t.Errorf("nil sub should render the collapsed Subscribe control:\n%s", nilOut)
	}
	if strings.Contains(nilOut, `hx-post="/models/42/unsubscribe"`) {
		t.Error("nil sub must not render Unsubscribe")
	}

	mid := 42
	sub := &store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true}
	subOut := renderString(t, modelDetailPage(view, sub, "csrf", "dark", "https://civitai.com"))
	if !strings.Contains(subOut, `hx-post="/models/42/unsubscribe"`) {
		t.Errorf("non-nil sub should render Unsubscribe:\n%s", subOut)
	}
}

// TestSearchCardsReflectSubscribedState proves that in a search-result grid the
// subscribed model's card renders the Unsubscribe/subscribed state while the
// not-subscribed model's card renders the collapsed Subscribe.
func TestSearchCardsReflectSubscribedState(t *testing.T) {
	res := &civitai.ModelSearchResult{Items: []civitai.ModelListItem{
		{ID: 10, Name: "Subbed", Type: "LORA"},
		{ID: 20, Name: "Unsubbed", Type: "LORA"},
	}}
	mid := 10
	subs := map[int]*store.Subscription{10: {Kind: store.KindModel, ModelID: &mid, AutoDownload: true}}

	out := renderString(t, searchResults(res, subs, fullMaturityRange(), "csrf", ""))
	// Subscribed card (10) → Unsubscribe.
	if !strings.Contains(out, `hx-post="/models/10/unsubscribe"`) {
		t.Errorf("subscribed card should render Unsubscribe:\n%s", out)
	}
	// Not-subscribed card (20) → collapsed Subscribe.
	if !strings.Contains(out, `hx-get="/models/20/subscribe-options"`) {
		t.Errorf("not-subscribed card should render the collapsed Subscribe:\n%s", out)
	}
	// The not-subscribed model must NOT get an unsubscribe control.
	if strings.Contains(out, `hx-post="/models/20/unsubscribe"`) {
		t.Error("not-subscribed card must not render Unsubscribe")
	}
}

// TestSearchHandlerReflectsSubscribedState exercises the FULL handleSearch path:
// with a subscription persisted for one of the returned models, the /search render
// reflects the correct per-card state from ONE modelSubscriptions() build.
func TestSearchHandlerReflectsSubscribedState(t *testing.T) {
	srv := subReflectServer(t, []civitai.ModelListItem{
		{ID: 10, Name: "Subbed", Type: "LORA"},
		{ID: 20, Name: "Unsubbed", Type: "LORA"},
	})
	seedModelSub(t, srv.store, 10)

	rec := get(t, srv, "/search?q=foo")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `hx-post="/models/10/unsubscribe"`) {
		t.Errorf("search card for the subscribed model should render Unsubscribe:\n%s", out)
	}
	if !strings.Contains(out, `hx-get="/models/20/subscribe-options"`) {
		t.Errorf("search card for the not-subscribed model should render collapsed Subscribe:\n%s", out)
	}
}

// TestCreatorHandlerReflectsSubscribedState proves the creator page's model cards
// likewise reflect real per-card subscribe state.
func TestCreatorHandlerReflectsSubscribedState(t *testing.T) {
	srv := subReflectServer(t, []civitai.ModelListItem{
		{ID: 10, Name: "Subbed", Type: "LORA"},
		{ID: 20, Name: "Unsubbed", Type: "LORA"},
	})
	seedModelSub(t, srv.store, 10)

	rec := get(t, srv, "/creators/alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /creators/alice = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `hx-post="/models/10/unsubscribe"`) {
		t.Errorf("creator card for the subscribed model should render Unsubscribe:\n%s", out)
	}
	if !strings.Contains(out, `hx-get="/models/20/subscribe-options"`) {
		t.Errorf("creator card for the not-subscribed model should render collapsed Subscribe:\n%s", out)
	}
}

// TestSuggestionsRenderCollapsedSubscribe proves the library suggestion cards —
// which exclude already-subscribed models — keep rendering the collapsed Subscribe
// control (nil state), unchanged by this fix.
func TestSuggestionsRenderCollapsedSubscribe(t *testing.T) {
	out := renderString(t, suggestionsList([]suggestion{{ModelID: 5, FileCount: 1, TotalBytes: 1024, Name: "S"}}, "csrf"))
	if !strings.Contains(out, `hx-get="/models/5/subscribe-options"`) {
		t.Errorf("suggestion card should render the collapsed Subscribe control:\n%s", out)
	}
	if strings.Contains(out, `hx-post="/models/5/unsubscribe"`) {
		t.Error("suggestion card must never render Unsubscribe (suggestions exclude subscribed models)")
	}
}
