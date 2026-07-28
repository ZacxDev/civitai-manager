package comfy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestReadBoundedExactCapSucceeds pins the boundary: a body of EXACTLY max bytes
// is a legitimate read, not an overflow.
func TestReadBoundedExactCapSucceeds(t *testing.T) {
	body := strings.Repeat("a", 64)
	data, err := readBounded(strings.NewReader(body), 64)
	if err != nil {
		t.Fatalf("readBounded at exactly the cap: %v", err)
	}
	if string(data) != body {
		t.Errorf("data = %q, want the full body", data)
	}
}

// TestReadBoundedOverflowErrorsAndTruncates asserts cap+1 bytes is detected (not
// silently truncated) AND that the truncated bytes still come back, so the
// error-snippet call sites keep working.
func TestReadBoundedOverflowErrorsAndTruncates(t *testing.T) {
	body := strings.Repeat("b", 65)
	data, err := readBounded(strings.NewReader(body), 64)
	if err == nil {
		t.Fatal("readBounded over the cap returned nil error (silent truncation)")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("err = %v, want ErrResponseTooLarge", err)
	}
	if len(data) != 64 {
		t.Errorf("len(data) = %d, want the truncated 64 bytes", len(data))
	}
}

func TestReadBoundedEmptyBody(t *testing.T) {
	data, err := readBounded(bytes.NewReader(nil), 8)
	if err != nil {
		t.Fatalf("readBounded(empty): %v", err)
	}
	if len(data) != 0 {
		t.Errorf("len(data) = %d, want 0", len(data))
	}
}

// TestClientViewRejectsOversizedImage is the client-level proof: a /view body
// larger than maxImageBytes fails loudly instead of returning a truncated
// (corrupt) image that the capture path would store as if fine.
func TestClientViewRejectsOversizedImage(t *testing.T) {
	// Stream just over the cap without materializing it twice.
	chunk := bytes.Repeat([]byte{0x41}, 1<<20)
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/view": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			written := int64(0)
			for written <= maxImageBytes {
				n, err := w.Write(chunk)
				if err != nil {
					return
				}
				written += int64(n)
			}
		},
	})
	_, _, err := c.View(context.Background(), ImageRef{Filename: "huge.png", Type: "output"})
	if err == nil {
		t.Fatal("View of an oversized body returned nil error (silent truncation)")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	// The message must name the file so the capture warn log is actionable.
	if !strings.Contains(err.Error(), "huge.png") {
		t.Errorf("err = %v, want the filename in the message", err)
	}
}

// TestClientViewAcceptsNormalImage is the negative control: a normal-sized body
// still round-trips unchanged.
func TestClientViewAcceptsNormalImage(t *testing.T) {
	c, _ := fakeComfy(t, map[string]http.HandlerFunc{
		"/view": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNGBYTES"))
		},
	})
	data, ct, err := c.View(context.Background(), ImageRef{Filename: "a.png", Type: "output"})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if string(data) != "PNGBYTES" || ct != "image/png" {
		t.Errorf("data=%q ct=%q", data, ct)
	}
}
