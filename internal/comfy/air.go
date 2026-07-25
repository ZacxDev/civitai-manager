package comfy

import (
	"fmt"
	"strings"
)

// AIR (AI Resource) URN helpers. The CivitAI orchestration API identifies every
// resource a cloud run references by an AIR URN string. This file builds those
// URNs from the local model metadata (model type + version's base model) the
// library already caches.
//
// Grammar (verified from civitai/src/shared/utils/air.ts):
//
//	urn:air:<ecosystem>:<type>:civitai:<modelId>@<versionId>
//
// The maps here are deliberately small and pragmatic: they cover the common
// baseModels/model types and fall back to a best-effort, flagged value for the
// long tail (the user can edit an unresolved/incorrect URN in the UI).

// ecosystemKeys maps a CivitAI baseModel string to its AIR ecosystem key. The
// keys here are already lowercase (AIR ecosystems are lowercased). A baseModel
// absent from this map is handled by EcosystemKey's best-effort fallback.
var ecosystemKeys = map[string]string{
	"SD 1.5":         "sd1",
	"SD 1.4":         "sd1",
	"SD 2.0":         "sd2",
	"SD 2.1":         "sd2",
	"SD 3":           "sd3",
	"SD 3.5":         "sd3",
	"SD 3.5 Medium":  "sd3_5m",
	"SDXL 1.0":       "sdxl",
	"SDXL":           "sdxl",
	"SDXL Turbo":     "sdxl",
	"SDXL Lightning": "sdxl",
	"Pony":           "sdxl",
	"Illustrious":    "illustrious",
	"NoobAI":         "noobai",
	"Flux.1 D":       "flux1",
	"Flux.1 S":       "flux1",
	"Flux.1":         "flux1",
	"Flux.1 Kontext": "flux1kontext",
}

// urnTypes maps a CivitAI ModelType to its AIR type segment. An unknown type
// yields "unknown".
var urnTypes = map[string]string{
	"Checkpoint":        "checkpoint",
	"LORA":              "lora",
	"LoCon":             "lycoris",
	"DoRA":              "dora",
	"TextualInversion":  "embedding",
	"VAE":               "vae",
	"Controlnet":        "controlnet",
	"Upscaler":          "upscaler",
	"Hypernetwork":      "hypernet",
	"MotionModule":      "motion",
	"AestheticGradient": "ag",
	"TextEncoder":       "text_encoders",
	"UNet":              "unet",
	"CLIPVision":        "clipvision",
	"CLIP":              "clip",
}

// EcosystemKey maps a baseModel string to its AIR ecosystem key. known is true
// when the baseModel is in the explicit map; when false the returned key is a
// best-effort, sanitized lowercase form of the baseModel (spaces removed) so a
// URN can still be assembled and flagged for user review. An empty baseModel
// yields ("unknown", false).
func EcosystemKey(baseModel string) (key string, known bool) {
	bm := strings.TrimSpace(baseModel)
	if bm == "" {
		return "unknown", false
	}
	if k, ok := ecosystemKeys[bm]; ok {
		return k, true
	}
	return sanitizeEcosystem(bm), false
}

// sanitizeEcosystem produces a best-effort ecosystem key from an unmapped
// baseModel: lowercased, spaces removed, and any character outside [a-z0-9_.]
// dropped so the fallback can never inject URN-breaking punctuation.
func sanitizeEcosystem(bm string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(bm) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '-':
			// drop spaces/hyphens (collapse "flux dev" → "fluxdev")
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// URNType maps a CivitAI ModelType to its AIR type segment; an unknown/empty
// type yields "unknown".
func URNType(modelType string) string {
	if t, ok := urnTypes[strings.TrimSpace(modelType)]; ok {
		return t
	}
	return "unknown"
}

// BuildCivitaiAIR assembles a CivitAI model-resource AIR URN from its parts:
//
//	urn:air:<ecosystem>:<type>:civitai:<modelID>@<versionID>
//
// ecosystem and urnType are used verbatim (callers pass the outputs of
// EcosystemKey / URNType).
func BuildCivitaiAIR(ecosystem, urnType string, modelID, versionID int) string {
	return fmt.Sprintf("urn:air:%s:%s:civitai:%d@%d", ecosystem, urnType, modelID, versionID)
}
