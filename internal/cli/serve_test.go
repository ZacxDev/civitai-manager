package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeClient is an offline civitai.Client (Reader + Downloader) for driving
// serveRun without any network.
type fakeClient struct{}

func (fakeClient) GetModel(context.Context, string) (*civitai.ModelDetail, []byte, error) {
	return &civitai.ModelDetail{}, nil, nil
}
func (fakeClient) GetModelVersion(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return &civitai.ModelVersionDetail{}, nil, nil
}
func (fakeClient) GetModelVersionByHash(context.Context, string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}
func (fakeClient) GetModelVersionsByHashes(context.Context, []string) ([]civitai.HashMatch, error) {
	return nil, nil
}
func (fakeClient) SearchModels(context.Context, url.Values) (*civitai.ModelSearchResult, error) {
	return &civitai.ModelSearchResult{}, nil
}
func (fakeClient) SearchCreators(context.Context, url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}
func (fakeClient) SearchImages(context.Context, url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}
func (fakeClient) DownloadFile(context.Context, string) (*http.Response, error) {
	return nil, errors.New("no downloads in test")
}

// TestServeRunCleanShutdown proves finding #6: on shutdown serveRun stops the
// HTTP server, awaits the poller and worker goroutines, and returns without
// error — and it does NOT close the store (so the caller's final writes are
// safe and there is no "database is closed" race).
func TestServeRunCleanShutdown(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		Addr:                "127.0.0.1:0", // ephemeral port
		BaseURL:             "https://civitai.com",
		DefaultPollInterval: config.Duration(time.Hour),
		DownloadJitter:      config.Duration(15 * time.Minute),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveRun(ctx, st, fakeClient{}, cfg, log) }()

	// Trigger shutdown; serveRun must tear down and return promptly.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveRun clean shutdown returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveRun did not shut down within 10s (goroutine wait deadlock?)")
	}

	// The store must still be open: serveRun awaited the goroutines before
	// returning and left closing to the caller.
	if _, err := st.ListSubscriptions(); err != nil {
		t.Fatalf("store should remain open after serveRun returns: %v", err)
	}
}

func TestDisplayAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8787": "127.0.0.1:8787",
		":8787":          "localhost:8787",
		"0.0.0.0:8787":   "localhost:8787",
		"192.168.1.5:80": "192.168.1.5:80",
	}
	for in, want := range cases {
		if got := displayAddr(in); got != want {
			t.Errorf("displayAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
