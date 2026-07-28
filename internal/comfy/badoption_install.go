package comfy

import "strings"

// IsModelFileValue reports whether a bad-option value is a model FILE reference
// (it carries a known model extension) rather than an inert enum label. It is the
// deterministic split the UI uses to decide whether an "Install <file>" action can
// resolve+download the exact file (model-file bad option) vs a pick-only substitute
// (an inert enum drift like a wildcard-picker label). It uses the SAME extension set
// as the loader-input detection so the two flows agree.
func IsModelFileValue(v string) bool {
	return hasModelExtension(v)
}

// InferBadOptionInstall maps a model-file BadOption — its node class_type, the combo
// input's name, and the current (invalid) value — to its install routing: the CivitAI
// models-list `types` value to attempt a CivitAI resolution with, and the ComfyUI
// models/ subdirectory the file lands in. ok is false when neither can be determined
// (then NO Install action is offered — we never guess a destination directory).
//
//   - UltralyticsDetectorProvider.model_name (Impact Pack) routes to
//     models/ultralytics/{bbox,segm} by the value's bbox//segm/ prefix. It has NO
//     CivitAI type (civitaiType == ""): the file is resolved via the HuggingFace
//     curated adetailer repo, not a CivitAI type search. The subdir here MUST agree
//     with the HF curated map's ultralytics/{bbox,segm} routing.
//   - Standard loader-style inputs (ckpt_name, vae_name, lora_name, control_net_name,
//     unet_name, embeddings, …) reuse InferCivitaiType → TypeSubdir. An input whose
//     CivitAI type has no ComfyUI home yields ok=false.
func InferBadOptionInstall(classType, inputName, current string) (civitaiType, subdir string, ok bool) {
	if isUltralyticsDetectorInput(classType, inputName) {
		return "", detectorSubdirForValue(current), true
	}
	t := InferCivitaiType(inputName, classType)
	if sub, ok := TypeSubdir(t); ok {
		return t, sub, true
	}
	return "", "", false
}

// isUltralyticsDetectorInput recognizes the Impact-Pack UltralyticsDetectorProvider's
// model_name combo, whose values are subfolder-prefixed detector files
// (e.g. "bbox/face_yolov9c.pt") that live under models/ultralytics.
func isUltralyticsDetectorInput(classType, inputName string) bool {
	return strings.EqualFold(strings.TrimSpace(inputName), "model_name") &&
		strings.Contains(classType, "Ultralytics")
}

// detectorSubdirForValue routes an ultralytics detector reference to
// models/ultralytics/{bbox,segm} by its leading path segment (how Impact-Pack
// organizes its model_name values). A reference with no such prefix defaults to bbox
// (the common case). This mirrors the HuggingFace resolver's detectorSubdir so the
// CivitAI-type path and the HF path agree on the destination.
func detectorSubdirForValue(current string) string {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(current), "\\", "/"))
	if strings.HasPrefix(norm, "segm/") {
		return "ultralytics/segm"
	}
	return "ultralytics/bbox"
}
