package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// renderString renders a node to a string, failing on error.
func renderString(t *testing.T, n g.Node) string {
	t.Helper()
	var sb strings.Builder
	if err := n.Render(&sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestPagesRenderWithoutPanic(t *testing.T) {
	mid := 42
	subs := []store.Subscription{
		{ID: 1, Kind: store.KindModel, ModelID: &mid, AutoDownload: true, Layout: "default", PollIntervalSecs: 3600},
		{ID: 2, Kind: store.KindCreator, Username: "alice", NotifyOnly: true, Layout: "default", PollIntervalSecs: 7200},
	}

	t.Run("dashboard", func(t *testing.T) {
		out := renderString(t, dashboardPage(subs, "test-csrf"))
		for _, want := range []string{"Subscriptions", "Add a subscription", "Download queue", "Activity", "/assets/output.css", "/assets/htmx.min.js", "alice"} {
			if !strings.Contains(out, want) {
				t.Errorf("dashboard missing %q", want)
			}
		}
	})

	t.Run("search empty", func(t *testing.T) {
		out := renderString(t, searchPage("", nil, "https://civitai.com"))
		if !strings.Contains(out, "Search models") {
			t.Error("search page missing header")
		}
	})

	t.Run("search results", func(t *testing.T) {
		res := &civitai.ModelSearchResult{Items: []civitai.ModelListItem{
			{ID: 1, Name: "Cool LoRA", Type: "LORA", Creator: &civitai.Creator{Username: "bob"}},
		}}
		out := renderString(t, searchResults(res, ""))
		for _, want := range []string{"Cool LoRA", "LORA", "bob", "/models/1"} {
			if !strings.Contains(out, want) {
				t.Errorf("search results missing %q", want)
			}
		}
	})

	t.Run("model detail", func(t *testing.T) {
		m := &civitai.ModelDetail{ID: 7, Name: "Great Model", Type: "Checkpoint",
			Creator:       &civitai.Creator{Username: "carol"},
			ModelVersions: []civitai.ModelVersionSummary{{ID: 1, Name: "v1", BaseModel: "SDXL"}}}
		out := renderString(t, modelDetailPage(m, "test-csrf"))
		for _, want := range []string{"Great Model", "Versions", "v1", "SDXL", "Subscribe"} {
			if !strings.Contains(out, want) {
				t.Errorf("model detail missing %q", want)
			}
		}
	})

	t.Run("creator", func(t *testing.T) {
		res := &civitai.ModelSearchResult{Items: []civitai.ModelListItem{{ID: 9, Name: "M", Type: "LORA"}}}
		out := renderString(t, creatorPage("dave", res, "test-csrf"))
		if !strings.Contains(out, "@dave") || !strings.Contains(out, "Subscribe to creator") {
			t.Error("creator page missing key elements")
		}
	})

	t.Run("queue and events fragments", func(t *testing.T) {
		items := []store.QueueItem{{ID: 1, FileName: "a.safetensors", Status: store.StatusDownloading, BytesDone: 512, SizeKB: 1}}
		out := renderString(t, queueFragment(items))
		if !strings.Contains(out, "a.safetensors") || !strings.Contains(out, "downloading") {
			t.Error("queue fragment missing elements")
		}
		mid := 1
		vid := 2
		evs := []store.Event{{ID: 1, TS: time.Now(), Level: store.LevelInfo, Kind: "x", ModelID: &mid, VersionID: &vid, Message: "hello"}}
		out = renderString(t, eventsFragment(evs))
		if !strings.Contains(out, "hello") {
			t.Error("events fragment missing message")
		}
	})
}

// --- handler tests ---

type stubReader struct{}

func (stubReader) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return &civitai.ModelDetail{ID: 1, Name: "M", Type: "LORA"}, nil, nil
}
func (stubReader) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return &civitai.ModelVersionDetail{}, nil, nil
}
func (stubReader) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (stubReader) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (stubReader) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

type stubSubscriber struct{ err error }

func (s stubSubscriber) SubscribeModel(context.Context, int, poller.SubscribeOptions) (int64, error) {
	return 1, s.err
}
func (s stubSubscriber) SubscribeCreator(context.Context, string, poller.SubscribeOptions) (int64, error) {
	return 1, s.err
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, stubSubscriber{}, Config{BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour}, nil)
}

func TestDashboardHandler(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Subscriptions") {
		t.Error("dashboard body missing Subscriptions")
	}
}

func TestAssetsServed(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/assets/output.css", "/assets/htmx.min.js"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("asset %s status = %d", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("asset %s empty", path)
		}
	}
}

func TestSubscribeHandlerRendersTable(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	// A valid CSRF token is now required on state-changing POSTs.
	form := strings.NewReader("model=12345&auto_download=true&csrf_token=" + srv.csrf)
	req := httptest.NewRequest(http.MethodPost, "/subscribe", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "subscriptions-table") {
		t.Error("subscribe response should return the subscriptions table fragment")
	}
}

// TestCSRFProtection asserts state-changing POSTs are rejected without a valid
// token and accepted with one (header or form field).
func TestCSRFProtection(t *testing.T) {
	postForms := []struct {
		name string
		path string
		body string
	}{
		{"subscribe", "/subscribe", "model=12345&auto_download=true"},
		{"flags", "/subscriptions/1/flags", "auto_download=true&notify_only=false"},
		{"delete", "/subscriptions/1/delete", ""},
	}
	for _, tc := range postForms {
		t.Run(tc.name+" without token → 403", func(t *testing.T) {
			srv := newTestServer(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 without CSRF token, got %d", rec.Code)
			}
		})
		t.Run(tc.name+" with wrong token → 403", func(t *testing.T) {
			srv := newTestServer(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-CSRF-Token", "not-the-token")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 with wrong CSRF token, got %d", rec.Code)
			}
		})
		t.Run(tc.name+" with header token → accepted", func(t *testing.T) {
			srv := newTestServer(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-CSRF-Token", srv.csrf)
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("valid CSRF token should not be rejected (got 403)")
			}
		})
	}
}
