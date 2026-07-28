package comfy

import "testing"

// TestIsModelFileValue: the model-file vs inert-enum split keys off a model extension.
func TestIsModelFileValue(t *testing.T) {
	modelFiles := []string{
		"bbox/face_yolov9c.pt",
		"segm/person-seg.pt",
		"fabricatedXL_v70.safetensors",
		"vae-ft-mse-840000-ema-pruned.ckpt",
		"model.pth",
		"weights.bin",
		"flux.gguf",
		"UPPER.SAFETENSORS", // case-insensitive
	}
	for _, v := range modelFiles {
		if !IsModelFileValue(v) {
			t.Errorf("IsModelFileValue(%q) = false, want true (model file)", v)
		}
	}
	inert := []string{
		"Select Wildcard 🟢 Full Cache",
		"Select the Wildcard to add to the text",
		"euler",
		"normal",
		"",
		"some.random.label", // trailing token is not a model extension
	}
	for _, v := range inert {
		if IsModelFileValue(v) {
			t.Errorf("IsModelFileValue(%q) = true, want false (inert enum)", v)
		}
	}
}

// TestInferBadOptionInstall covers the (class,input,current) → (civitaiType,subdir)
// routing: the ultralytics detector special case (bbox/segm by prefix, no CivitAI
// type), the standard loader inputs via InferCivitaiType/TypeSubdir, and the
// unknown/unroutable → ok=false guard.
func TestInferBadOptionInstall(t *testing.T) {
	cases := []struct {
		name         string
		class, input string
		current      string
		wantType     string
		wantSubdir   string
		wantOK       bool
	}{
		{"detector bbox prefix", "UltralyticsDetectorProvider", "model_name", "bbox/face_yolov9c.pt", "", "ultralytics/bbox", true},
		{"detector segm prefix", "UltralyticsDetectorProvider", "model_name", "segm/person-seg.pt", "", "ultralytics/segm", true},
		{"detector no prefix defaults bbox", "UltralyticsDetectorProvider", "model_name", "face_yolov9c.pt", "", "ultralytics/bbox", true},
		{"detector case-insensitive prefix", "UltralyticsDetectorProvider", "model_name", "SEGM/Thing.pt", "", "ultralytics/segm", true},
		{"checkpoint", "CheckpointLoaderSimple", "ckpt_name", "foo.safetensors", "Checkpoint", "checkpoints", true},
		{"lora", "LoraLoader", "lora_name", "foo.safetensors", "LORA", "loras", true},
		{"vae", "VAELoader", "vae_name", "foo.safetensors", "VAE", "vae", true},
		{"controlnet", "ControlNetLoader", "control_net_name", "foo.safetensors", "Controlnet", "controlnet", true},
		{"unet routes via checkpoint type", "UNETLoader", "unet_name", "foo.safetensors", "Checkpoint", "checkpoints", true},
		{"embedding input", "SomeLoader", "embedding_name", "foo.pt", "TextualInversion", "embeddings", true},
		// A model_name on a NON-ultralytics node with no CivitAI-type mapping is not
		// routable (never guess a destination).
		{"unknown model_name node", "UpscaleModelLoader", "model_name", "RealESRGAN_x4plus.pth", "", "", false},
		{"unknown input", "MysteryNode", "mystery_input", "x.safetensors", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, sub, ok := InferBadOptionInstall(tc.class, tc.input, tc.current)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (type=%q subdir=%q)", ok, tc.wantOK, typ, sub)
			}
			if typ != tc.wantType {
				t.Errorf("civitaiType = %q, want %q", typ, tc.wantType)
			}
			if sub != tc.wantSubdir {
				t.Errorf("subdir = %q, want %q", sub, tc.wantSubdir)
			}
		})
	}
}
