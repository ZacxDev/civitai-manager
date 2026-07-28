package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfyext"
)

// TestExtensionPingDetectsHelper proves a well-formed helper response is accepted
// and its version surfaced.
func TestExtensionPingDetectsHelper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != extPingPath {
			t.Errorf("probe hit %q, want %q", r.URL.Path, extPingPath)
		}
		_, _ = io.WriteString(w, `{"tool":"civitai-manager","version":"1"}`)
	}))
	defer srv.Close()

	info, err := NewClient(srv.URL, "").ExtensionPing(context.Background())
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if info.Version != "1" {
		t.Errorf("version = %q, want 1", info.Version)
	}
}

// TestExtensionPingAbsentSignals proves every "not really our helper" shape —
// 404, 500, an HTML error page, valid JSON from another tool, a truncated body,
// and an unreachable server — is reported as ErrExtensionAbsent (never a
// false positive).
func TestExtensionPingAbsentSignals(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"404 (stock ComfyUI)": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"500": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"html error page with 200": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body>nope</body></html>")
		},
		"another tool answering": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"tool":"some-other-tool","version":"1"}`)
		},
		"empty body": func(w http.ResponseWriter, _ *http.Request) {},
		"oversized body": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"tool":"civitai-manager","pad":"`+strings.Repeat("x", maxExtBytes+64)+`"}`)
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			if _, err := NewClient(srv.URL, "").ExtensionPing(context.Background()); !errors.Is(err, ErrExtensionAbsent) {
				t.Errorf("err = %v, want ErrExtensionAbsent", err)
			}
		})
	}

	t.Run("unreachable server", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		if _, err := NewClient(url, "").ExtensionPing(context.Background()); !errors.Is(err, ErrExtensionAbsent) {
			t.Errorf("err = %v, want ErrExtensionAbsent", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"tool":"civitai-manager","version":"1"}`)
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewClient(srv.URL, "").ExtensionPing(ctx); !errors.Is(err, ErrExtensionAbsent) {
			t.Errorf("err = %v, want ErrExtensionAbsent (a timeout must fall back, not crash)", err)
		}
	})
}

// TestExtensionAssetAcceptsTheRealScript proves the frontend-half probe hits the
// URL ComfyUI serves the helper's script from and accepts the real script.
func TestExtensionAssetAcceptsTheRealScript(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `app.registerExtension({ name: "`+comfyext.AssetMarker+`" });`)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "").ExtensionAsset(context.Background()); err != nil {
		t.Fatalf("asset: %v", err)
	}
	if gotPath != comfyext.AssetURLPath {
		t.Errorf("asset probe hit %q, want %q", gotPath, comfyext.AssetURLPath)
	}
}

// TestExtensionAssetAbsentSignals proves every "the frontend script is not really
// being served" shape is ErrExtensionAbsent. The FIRST case is the live-caught
// zombie: the directory was deleted, so the static route 404s even though the
// in-memory python routes still answer.
func TestExtensionAssetAbsentSignals(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"404 (helper directory deleted — the zombie case)": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"500": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"empty body with 200": func(w http.ResponseWriter, _ *http.Request) {},
		"a proxy error page with 200": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body>502 upstream</body></html>")
		},
		"someone else's script": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `app.registerExtension({ name: "someone.else" });`)
		},
		"oversized body": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxExtAssetBytes+64))
		},
	}
	for name, hf := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(hf)
			defer srv.Close()
			if err := NewClient(srv.URL, "").ExtensionAsset(context.Background()); !errors.Is(err, ErrExtensionAbsent) {
				t.Errorf("err = %v, want ErrExtensionAbsent", err)
			}
		})
	}

	t.Run("unreachable server", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		if err := NewClient(url, "").ExtensionAsset(context.Background()); !errors.Is(err, ErrExtensionAbsent) {
			t.Errorf("err = %v, want ErrExtensionAbsent", err)
		}
	})

	t.Run("timeout / cancelled context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, comfyext.AssetMarker)
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := NewClient(srv.URL, "").ExtensionAsset(ctx); !errors.Is(err, ErrExtensionAbsent) {
			t.Errorf("err = %v, want ErrExtensionAbsent (a timeout must fall back, not crash)", err)
		}
	})
}

// TestExtensionOpenPostsPath proves the broadcast request carries the workflow
// path as JSON to the helper's open route.
func TestExtensionOpenPostsPath(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPath = body.Path
		if r.URL.Path != extOpenPath {
			t.Errorf("open hit %q, want %q", r.URL.Path, extOpenPath)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "").ExtensionOpen(context.Background(), "civitai-manager/wf-7.json"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "civitai-manager/wf-7.json" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestExtensionOpenRejectsEscapingPath proves a path that could escape the
// workflows directory never leaves the process.
func TestExtensionOpenRejectsEscapingPath(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	for _, bad := range []string{"", "   ", "/etc/passwd", `a\b.json`, "../escape.json", "a/../../b.json"} {
		if err := c.ExtensionOpen(context.Background(), bad); err == nil {
			t.Errorf("ExtensionOpen(%q) should be refused", bad)
		}
	}
	if reached {
		t.Error("a refused path must not reach ComfyUI")
	}
}

// TestExtensionOpen404IsAbsent proves a stock ComfyUI (no helper route) reports
// ErrExtensionAbsent so the caller can fall back honestly.
func TestExtensionOpen404IsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := NewClient(srv.URL, "").ExtensionOpen(context.Background(), "civitai-manager/wf-7.json")
	if !errors.Is(err, ErrExtensionAbsent) {
		t.Errorf("err = %v, want ErrExtensionAbsent", err)
	}
}
