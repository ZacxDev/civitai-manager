package comfy

import "testing"

func TestEcosystemKey(t *testing.T) {
	tests := []struct {
		baseModel string
		wantKey   string
		wantKnown bool
	}{
		{"SD 1.5", "sd1", true},
		{"SD 1.4", "sd1", true},
		{"SD 2.0", "sd2", true},
		{"SD 2.1", "sd2", true},
		{"SD 3", "sd3", true},
		{"SD 3.5", "sd3", true},
		{"SD 3.5 Medium", "sd3_5m", true},
		{"SDXL 1.0", "sdxl", true},
		{"SDXL", "sdxl", true},
		{"SDXL Turbo", "sdxl", true},
		{"SDXL Lightning", "sdxl", true},
		{"Pony", "sdxl", true},
		{"Illustrious", "illustrious", true},
		{"NoobAI", "noobai", true},
		{"Flux.1 D", "flux1", true},
		{"Flux.1 S", "flux1", true},
		{"Flux.1", "flux1", true},
		{"Flux.1 Kontext", "flux1kontext", true},
		// Unknown → best-effort sanitized fallback, known=false.
		{"Some Future Model", "somefuturemodel", false},
		{"Weird-Name 2", "weirdname2", false},
		{"", "unknown", false},
		{"   ", "unknown", false},
		{"!!!", "unknown", false},
	}
	for _, tc := range tests {
		gotKey, gotKnown := EcosystemKey(tc.baseModel)
		if gotKey != tc.wantKey || gotKnown != tc.wantKnown {
			t.Errorf("EcosystemKey(%q) = (%q, %v), want (%q, %v)",
				tc.baseModel, gotKey, gotKnown, tc.wantKey, tc.wantKnown)
		}
	}
}

func TestURNType(t *testing.T) {
	tests := []struct {
		modelType string
		want      string
	}{
		{"Checkpoint", "checkpoint"},
		{"LORA", "lora"},
		{"LoCon", "lycoris"},
		{"DoRA", "dora"},
		{"TextualInversion", "embedding"},
		{"VAE", "vae"},
		{"Controlnet", "controlnet"},
		{"Upscaler", "upscaler"},
		{"Hypernetwork", "hypernet"},
		{"MotionModule", "motion"},
		{"AestheticGradient", "ag"},
		{"TextEncoder", "text_encoders"},
		{"UNet", "unet"},
		{"CLIPVision", "clipvision"},
		{"CLIP", "clip"},
		{"NotAType", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range tests {
		if got := URNType(tc.modelType); got != tc.want {
			t.Errorf("URNType(%q) = %q, want %q", tc.modelType, got, tc.want)
		}
	}
}

func TestBuildCivitaiAIR(t *testing.T) {
	tests := []struct {
		ecosystem string
		urnType   string
		modelID   int
		versionID int
		want      string
	}{
		{"sdxl", "checkpoint", 12345, 67890, "urn:air:sdxl:checkpoint:civitai:12345@67890"},
		{"flux1", "lora", 1, 2, "urn:air:flux1:lora:civitai:1@2"},
		{"sd1", "vae", 999, 1000, "urn:air:sd1:vae:civitai:999@1000"},
	}
	for _, tc := range tests {
		if got := BuildCivitaiAIR(tc.ecosystem, tc.urnType, tc.modelID, tc.versionID); got != tc.want {
			t.Errorf("BuildCivitaiAIR(%q,%q,%d,%d) = %q, want %q",
				tc.ecosystem, tc.urnType, tc.modelID, tc.versionID, got, tc.want)
		}
	}
}

// TestBuildCivitaiAIR_EndToEnd walks the full resolution: baseModel + modelType →
// keys → assembled URN, the exact path ResolveResources uses.
func TestBuildCivitaiAIR_EndToEnd(t *testing.T) {
	eco, known := EcosystemKey("SDXL 1.0")
	if !known || eco != "sdxl" {
		t.Fatalf("ecosystem: got (%q,%v)", eco, known)
	}
	urn := BuildCivitaiAIR(eco, URNType("LORA"), 42, 43)
	if want := "urn:air:sdxl:lora:civitai:42@43"; urn != want {
		t.Fatalf("URN = %q, want %q", urn, want)
	}
}
