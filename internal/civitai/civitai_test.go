package civitai

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseModelRef(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"12345", 12345, false},
		{"  678 ", 678, false},
		{"https://civitai.com/models/4201/realistic-vision", 4201, false},
		{"https://civitai.com/models/999?modelVersionId=42", 999, false},
		{"civitai.com/models/50", 50, false},
		{"", 0, true},
		{"not-a-model", 0, true},
		{"-5", 0, true},
		{"0", 0, true},
	}
	for _, c := range cases {
		got, err := ParseModelRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseModelRef(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModelRef(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseModelRef(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSelectFile(t *testing.T) {
	files := []ModelVersionFile{
		{ID: 1, Name: "a.safetensors", Type: "Model", Primary: false},
		{ID: 2, Name: "b.safetensors", Type: "Model", Primary: true},
		{ID: 3, Name: "vae.pt", Type: "VAE", Primary: false},
	}
	if f := SelectFile(files, ""); f == nil || f.ID != 2 {
		t.Errorf("no pref: expected primary (id 2), got %+v", f)
	}
	if f := SelectFile(files, "VAE"); f == nil || f.ID != 3 {
		t.Errorf("VAE pref: expected id 3, got %+v", f)
	}
	if f := SelectFile(files, "Nonexistent"); f == nil || f.ID != 2 {
		t.Errorf("unmatched pref should fall back to primary, got %+v", f)
	}
	if f := SelectFile(nil, ""); f != nil {
		t.Errorf("empty files should yield nil, got %+v", f)
	}
}

func TestDestPathSanitizes(t *testing.T) {
	got := DestPath("/root", "LORA", "some/../creator", "My: Model?", "v1.0", "weights.safetensors")
	want := filepath.Join("/root", "LORA", "some_.._creator", "My_ Model_", "v1.0.safetensors")
	if got != want {
		t.Errorf("DestPath = %q, want %q", got, want)
	}
	// version name without extension borrows the file's extension.
	if !strings.HasSuffix(got, ".safetensors") {
		t.Errorf("expected .safetensors extension, got %q", got)
	}
}

func TestDestPathEmptyComponents(t *testing.T) {
	got := DestPath("/root", "", "", "", "", "file.bin")
	want := filepath.Join("/root", "unknown", "unknown", "unknown", "file.bin")
	if got != want {
		t.Errorf("DestPath empty = %q, want %q", got, want)
	}
}

func TestSidecarBase(t *testing.T) {
	if got := SidecarBase("/a/b/model.safetensors"); got != "/a/b/model" {
		t.Errorf("SidecarBase = %q", got)
	}
}

func TestFirstImageURL(t *testing.T) {
	raw := []byte(`{"id":1,"images":[{"url":""},{"url":"https://img/preview.png"}]}`)
	if got := FirstImageURL(raw); got != "https://img/preview.png" {
		t.Errorf("FirstImageURL = %q", got)
	}
	if got := FirstImageURL([]byte(`{"images":[]}`)); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := FirstImageURL([]byte(`not json`)); got != "" {
		t.Errorf("bad json should yield empty, got %q", got)
	}
}
