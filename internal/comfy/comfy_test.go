package comfy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

// chunk assembles one PNG chunk: length(4 BE) + type(4) + data + crc32(type+data).
func chunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// tEXt builds a tEXt chunk payload: keyword + 0x00 + value.
func tEXt(keyword, value string) []byte {
	data := append([]byte(keyword), 0)
	data = append(data, []byte(value)...)
	return chunk("tEXt", data)
}

// buildPNG assembles a minimal valid PNG stream from the given chunks, wrapped in
// signature + trailing IEND.
func buildPNG(chunks ...[]byte) []byte {
	var b bytes.Buffer
	b.Write(pngSignature)
	// A tiny IHDR so the stream resembles a real PNG (the parser tolerates its
	// absence, but include it for realism).
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 1) // width
	binary.BigEndian.PutUint32(ihdr[4:8], 1) // height
	ihdr[8] = 8                              // bit depth
	ihdr[9] = 6                              // color type RGBA
	b.Write(chunk("IHDR", ihdr))
	for _, c := range chunks {
		b.Write(c)
	}
	b.Write(chunk("IEND", nil))
	return b.Bytes()
}

const sampleAPIGraph = `{"3":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sdxl.safetensors"}}}`
const sampleUIGraph = `{"nodes":[{"id":3,"type":"CheckpointLoaderSimple"}],"links":[]}`

func TestExtractFromPNG_BothGraphs(t *testing.T) {
	png := buildPNG(tEXt("prompt", sampleAPIGraph), tEXt("workflow", sampleUIGraph))
	ex, err := ExtractFromPNG(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if string(ex.APIGraph) != sampleAPIGraph {
		t.Errorf("APIGraph = %q", ex.APIGraph)
	}
	if string(ex.UIGraph) != sampleUIGraph {
		t.Errorf("UIGraph = %q", ex.UIGraph)
	}
	if ex.IsA1111 {
		t.Errorf("IsA1111 should be false")
	}
}

func TestExtractFromPNG_PromptOnly(t *testing.T) {
	png := buildPNG(tEXt("prompt", sampleAPIGraph))
	ex, err := ExtractFromPNG(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if string(ex.APIGraph) != sampleAPIGraph || ex.UIGraph != nil {
		t.Errorf("expected api-only, got api=%q ui=%q", ex.APIGraph, ex.UIGraph)
	}
}

func TestExtractFromPNG_A1111Only(t *testing.T) {
	png := buildPNG(tEXt("parameters", "masterpiece, best quality\nSteps: 20, Sampler: Euler"))
	ex, err := ExtractFromPNG(bytes.NewReader(png))
	if !errors.Is(err, ErrA1111Only) {
		t.Fatalf("err = %v, want ErrA1111Only", err)
	}
	if !ex.IsA1111 {
		t.Errorf("IsA1111 should be true")
	}
}

func TestExtractFromPNG_NoWorkflow(t *testing.T) {
	png := buildPNG(tEXt("Software", "some editor"))
	_, err := ExtractFromPNG(bytes.NewReader(png))
	if !errors.Is(err, ErrNoWorkflow) {
		t.Fatalf("err = %v, want ErrNoWorkflow", err)
	}
}

func TestExtractFromPNG_BadSignature(t *testing.T) {
	_, err := ExtractFromPNG(bytes.NewReader([]byte("not a png at all")))
	if !errors.Is(err, ErrInvalidPNG) {
		t.Fatalf("err = %v, want ErrInvalidPNG", err)
	}
}

func TestExtractFromPNG_Truncated(t *testing.T) {
	png := buildPNG(tEXt("prompt", sampleAPIGraph))
	// Cut off mid-stream (keep the signature + a partial chunk) so a chunk read
	// runs off the end.
	truncated := png[:len(pngSignature)+10]
	_, err := ExtractFromPNG(bytes.NewReader(truncated))
	if !errors.Is(err, ErrInvalidPNG) {
		t.Fatalf("err = %v, want ErrInvalidPNG", err)
	}
}

func TestExtractFromPNG_NonJSONPromptIgnored(t *testing.T) {
	// A `prompt` value that is not JSON must NOT become a stored graph; with no
	// other comfy metadata this is a no-workflow PNG.
	png := buildPNG(tEXt("prompt", "just a caption"))
	_, err := ExtractFromPNG(bytes.NewReader(png))
	if !errors.Is(err, ErrNoWorkflow) {
		t.Fatalf("err = %v, want ErrNoWorkflow", err)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
		err  bool
	}{
		{"api", sampleAPIGraph, FormatAPI, false},
		{"ui", sampleUIGraph, FormatUI, false},
		{"empty object", `{}`, "", true},
		{"garbage", `not json`, "", true},
		{"array", `[1,2,3]`, "", true},
		{"nodes-not-array", `{"nodes":"oops"}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectFormat([]byte(tc.json))
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got format %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("format = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractResources(t *testing.T) {
	graph := `{
		"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"dreamshaper.safetensors"}},
		"2":{"class_type":"LoraLoader","inputs":{"lora_name":"detail.safetensors","strength_model":0.8,"model":["1",0]}},
		"3":{"class_type":"VAELoader","inputs":{"vae_name":"vae.pt"}},
		"4":{"class_type":"KSampler","inputs":{"seed":42,"steps":20}},
		"5":{"class_type":"CLIPTextEncode","inputs":{"text":"a cat"}},
		"6":{"class_type":"MyCustomLoader","inputs":{"file":"custom.gguf"}}
	}`
	got, err := ExtractResources([]byte(graph))
	if err != nil {
		t.Fatalf("extract resources: %v", err)
	}
	want := map[string]bool{
		"dreamshaper.safetensors": true,
		"detail.safetensors":      true,
		"vae.pt":                  true,
		"custom.gguf":             true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d resources %v, want %d", len(got), got, len(want))
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected resource %q", r)
		}
	}
	// Non-model strings (prompt text, "a cat") and numeric inputs must be ignored.
	for _, r := range got {
		if r == "a cat" {
			t.Errorf("prompt text leaked into resources")
		}
	}
}

func TestExtractResources_Dedup(t *testing.T) {
	graph := `{
		"1":{"class_type":"LoraLoader","inputs":{"lora_name":"same.safetensors"}},
		"2":{"class_type":"LoraLoader","inputs":{"lora_name":"same.safetensors"}}
	}`
	got, err := ExtractResources([]byte(graph))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(got) != 1 || got[0] != "same.safetensors" {
		t.Errorf("dedup failed: %v", got)
	}
}

func TestExtractResources_Garbage(t *testing.T) {
	// Unparseable graph → no resources, no error (advisory extraction).
	got, err := ExtractResources([]byte(`not json`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no resources, got %v", got)
	}
}
