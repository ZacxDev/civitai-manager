package comfy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	// A malicious/escaping subdir must be refused even though real callers pass
	// hardcoded constants (defense in depth).
	for _, sub := range []string{"..", "../evil", "../../etc", "/abs/evil", `..\evil`} {
		if _, err := SafeModelDest(root, sub, "x.safetensors"); err == nil {
			t.Errorf("SafeModelDest subdir %q should have been refused", sub)
		}
	}
	// A legitimate nested subdir (the detector routing) is still accepted.
	if _, err := SafeModelDest(root, "ultralytics/bbox", "face_yolov8m.pt"); err != nil {
		t.Errorf("nested subdir ultralytics/bbox should be accepted: %v", err)
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

// sha256Hex returns the lowercase hex sha256 of data (test helper).
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestWriteModelStreamVerifiedCorrectHash: a matching expected sha256 writes the file.
func TestWriteModelStreamVerifiedCorrectHash(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ultralytics", "bbox")
	dest := filepath.Join(sub, "face_yolov9c.pt")
	data := []byte("pretend model weights")

	n, err := WriteModelStreamVerified(dest, bytes.NewReader(data), int64(len(data)), 0, sha256Hex(data))
	if err != nil {
		t.Fatalf("WriteModelStreamVerified: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("wrote %d, want %d", n, len(data))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content = %q, want %q", got, data)
	}
	assertNoTempLeftovers(t, sub)
}

// TestWriteModelStreamVerifiedWrongHash: a mismatched expected sha256 deletes the
// temp, writes NO file, and returns ErrHashMismatch — an unverified file never lands.
func TestWriteModelStreamVerifiedWrongHash(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "vae")
	dest := filepath.Join(sub, "x.safetensors")
	data := []byte("actual bytes")
	wrong := sha256Hex([]byte("different bytes entirely"))

	_, err := WriteModelStreamVerified(dest, bytes.NewReader(data), int64(len(data)), 0, wrong)
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("no file must be written on a sha256 mismatch")
	}
	assertNoTempLeftovers(t, sub)
}

// TestWriteModelStreamVerifiedUppercaseExpected: the expected digest comparison is
// case-insensitive (LFS oids are lowercase, but be robust to an uppercase input).
func TestWriteModelStreamVerifiedUppercaseExpected(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "loras", "y.safetensors")
	data := []byte("weights")
	up := strings.ToUpper(sha256Hex(data))
	if _, err := WriteModelStreamVerified(dest, bytes.NewReader(data), int64(len(data)), 0, up); err != nil {
		t.Fatalf("uppercase expected sha should still verify: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}

// TestWriteModelStreamNoHashUnchanged: the civitai path (empty expected sha) writes
// the file with no verification — behavior unchanged.
func TestWriteModelStreamNoHashUnchanged(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "checkpoints", "m.safetensors")
	data := []byte("no hash to pin here")
	if _, err := WriteModelStream(dest, bytes.NewReader(data), int64(len(data)), 0); err != nil {
		t.Fatalf("WriteModelStream (no hash): %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Errorf("content = %q, want %q", got, data)
	}
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
