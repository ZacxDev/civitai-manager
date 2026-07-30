package hf

import "testing"

// TestCuratedFamilyMatch pins the PURE render-time predicate: curated families match,
// the detector bbox//segm/ prefix matches (searchSubdir routes on it, so such a ref can
// auto-install), and a name with no routing rule does not.
func TestCuratedFamilyMatch(t *testing.T) {
	for name, tc := range map[string]struct {
		ref  string
		want bool
	}{
		"curated detector":     {"bbox/face_yolov9c.pt", true},
		"curated no prefix":    {"face_yolov8n.pt", true},
		"curated vae":          {"sdxl_vae.safetensors", true},
		"curated ip-adapter":   {"ip-adapter_sdxl.safetensors", true},
		"non-curated bbox":     {"bbox/my_custom_det.pt", true},
		"non-curated segm":     {"segm/my_custom_seg.pt", true},
		"windows-style prefix": {`bbox\my_custom_det.pt`, true},
		"uppercase prefix":     {"BBOX/My_Custom.pt", true},
		"no rule at all":       {"mystery.bin", false},
		"bbox not a prefix":    {"models/bbox-ish.pt", false},
		"empty":                {"", false},
		"dot":                  {".", false},
		"dotdot":               {"..", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CuratedFamilyMatch(tc.ref); got != tc.want {
				t.Errorf("CuratedFamilyMatch(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}
