package comfy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTypeSubdir covers the type→subdir routing table, case-insensitivity, and
// the unknown-type refusal.
func TestTypeSubdir(t *testing.T) {
	cases := []struct {
		typ     string
		wantSub string
		wantOK  bool
	}{
		{"Checkpoint", "checkpoints", true},
		{"checkpoint", "checkpoints", true},
		{"  LORA  ", "loras", true},
		{"LoCon", "loras", true},
		{"LyCORIS", "loras", true},
		{"VAE", "vae", true},
		{"Controlnet", "controlnet", true},
		{"TextualInversion", "embeddings", true},
		{"Upscaler", "upscale_models", true},
		{"Hypernetwork", "hypernetworks", true},
		{"Workflows", "", false},
		{"Poses", "", false},
		{"", "", false},
		{"nonsense", "", false},
	}
	for _, c := range cases {
		sub, ok := TypeSubdir(c.typ)
		if sub != c.wantSub || ok != c.wantOK {
			t.Errorf("TypeSubdir(%q) = (%q,%v), want (%q,%v)", c.typ, sub, ok, c.wantSub, c.wantOK)
		}
	}
}

// TestSafeModelDestBasenameOnly asserts the destination is always root/subdir/
// <basename> and that directory/traversal components are stripped.
func TestSafeModelDestBasenameOnly(t *testing.T) {
	root := filepath.Join("/comfy", "models")
	want := filepath.Join(root, "checkpoints", "model.safetensors")
	cases := []string{
		"model.safetensors",
		"seg-a/model.safetensors",
		"../../model.safetensors",
		"/abs/path/model.safetensors",
		"..\\..\\model.safetensors",
		"nested/deeper/model.safetensors",
	}
	for _, ref := range cases {
		got, err := SafeModelDest(root, "checkpoints", ref)
		if err != nil {
			t.Errorf("SafeModelDest(%q) error: %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("SafeModelDest(%q) = %q, want %q", ref, got, want)
		}
		// Hard invariant: the result never escapes root/subdir.
		base := filepath.Join(root, "checkpoints")
		if rel, _ := filepath.Rel(base, got); strings.HasPrefix(rel, "..") {
			t.Errorf("SafeModelDest(%q) escaped %s: rel=%q", ref, base, rel)
		}
	}
}

// TestSafeModelDestRejects covers the degenerate references and empty inputs.
func TestSafeModelDestRejects(t *testing.T) {
	root := "/comfy/models"
	for _, ref := range []string{"", "   ", ".", "..", "/", "foo/.."} {
		if _, err := SafeModelDest(root, "loras", ref); err == nil {
			t.Errorf("SafeModelDest(%q) should have errored", ref)
		}
	}
	if _, err := SafeModelDest("", "loras", "x.safetensors"); err == nil {
		t.Error("empty root should error")
	}
	if _, err := SafeModelDest(root, "", "x.safetensors"); err == nil {
		t.Error("empty subdir should error")
	}
}

// TestWriteModelStreamHappyPath: writes to the right path atomically (no temp
// leftovers) with the exact bytes.
func TestWriteModelStreamHappyPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "checkpoints")
	dest := filepath.Join(sub, "model.safetensors")
	data := []byte("hello safetensors")

	n, err := WriteModelStream(dest, bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		t.Fatalf("WriteModelStream: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("wrote %d bytes, want %d", n, len(data))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("dest content = %q, want %q", got, data)
	}
	assertNoTempLeftovers(t, sub)
}

// TestWriteModelStreamExisting asserts an existing destination is not clobbered.
func TestWriteModelStreamExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "loras", "x.safetensors")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WriteModelStream(dest, bytes.NewReader([]byte("new")), 3, 0)
	if !errors.Is(err, ErrDestExists) {
		t.Fatalf("err = %v, want ErrDestExists", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "original" {
		t.Errorf("existing file was clobbered: %q", got)
	}
}

// TestWriteModelStreamSizeCapAdvertised: an advertised Content-Length over the
// cap is rejected before any write.
func TestWriteModelStreamSizeCapAdvertised(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "checkpoints", "big.safetensors")
	_, err := WriteModelStream(dest, bytes.NewReader([]byte("data")), 1000, 100)
	if !errors.Is(err, ErrSizeCapExceeded) {
		t.Fatalf("err = %v, want ErrSizeCapExceeded", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("no file should be written when the advertised size exceeds the cap")
	}
	assertNoTempLeftovers(t, filepath.Join(dir, "checkpoints"))
}

// TestWriteModelStreamSizeCapLyingLength: a body larger than the cap with an
// absent/understated Content-Length is caught by the bounded copy, and the temp
// file is cleaned up.
func TestWriteModelStreamSizeCapLyingLength(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "loras")
	dest := filepath.Join(sub, "sneaky.safetensors")
	// contentLength unknown (-1) but the body is 500 bytes over a 100-byte cap.
	body := bytes.Repeat([]byte("A"), 600)
	_, err := WriteModelStream(dest, bytes.NewReader(body), -1, 100)
	if !errors.Is(err, ErrSizeCapExceeded) {
		t.Fatalf("err = %v, want ErrSizeCapExceeded", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("no final file should exist after an over-cap body")
	}
	assertNoTempLeftovers(t, sub)
}

// TestWriteModelStreamCleansTempOnReadError: a mid-stream read error removes the
// temp file and leaves no destination.
func TestWriteModelStreamCleansTempOnReadError(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "vae")
	dest := filepath.Join(sub, "v.safetensors")
	r := io.MultiReader(bytes.NewReader([]byte("partial")), &errReader{})
	_, err := WriteModelStream(dest, r, -1, 0)
	if err == nil {
		t.Fatal("expected a stream error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("no final file should exist after a read error")
	}
	assertNoTempLeftovers(t, sub)
}

// errReader always fails, simulating a truncated/broken download body.
type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// assertNoTempLeftovers fails if any .cm-download-* temp file remains in dir.
func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cm-download-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
