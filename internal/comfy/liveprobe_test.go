package comfy

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestLiveObjectInfoClassification is an OPT-IN probe against a REAL /object_info
// body, run with:
//
//	CM_LIVE_OBJECT_INFO=/path/to/object_info.json go test ./internal/comfy -run Live -v
//
// It is not a guard and proves nothing when skipped — the hermetic guards in
// node_origin_test.go are what gate the build. This exists because this repo has
// shipped three green fake-reader integrations that were broken against reality.
func TestLiveObjectInfoClassification(t *testing.T) {
	path := os.Getenv("CM_LIVE_OBJECT_INFO")
	if path == "" {
		t.Skip("set CM_LIVE_OBJECT_INFO to a real /object_info body to run this probe")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live payload: %v", err)
	}
	idx := NodeOriginsFromJSON(raw)
	if len(idx) == 0 {
		t.Fatalf("live payload decoded to an EMPTY index — the probe is broken, not the code")
	}

	var builtin, custom, unknown int
	for _, o := range idx {
		switch o {
		case NodeOriginBuiltin:
			builtin++
		case NodeOriginCustom:
			custom++
		default:
			unknown++
		}
	}
	t.Logf("live payload: %d bytes, %d node types", len(raw), len(idx))
	t.Logf("builtin=%d custom=%d unknown=%d", builtin, custom, unknown)

	// The two named misfires from the bug report are the ground truth.
	for _, tc := range []struct {
		class string
		want  NodeOrigin
	}{
		{"WanImageToVideo", NodeOriginBuiltin},
		{"CLIPVisionLoader", NodeOriginBuiltin},
		{"KSampler", NodeOriginBuiltin},
	} {
		if got := OriginOf(idx, tc.class); got != tc.want {
			t.Errorf("OriginOf(%q) = %v, want %v", tc.class, got, tc.want)
		}
	}

	// How much of coreNodeClasses the live payload can actually classify.
	var covered, absent []string
	for ct := range coreNodeClasses {
		if OriginOf(idx, ct) == NodeOriginUnknown {
			absent = append(absent, ct)
		} else {
			covered = append(covered, ct)
		}
	}
	sort.Strings(absent)
	t.Logf("coreNodeClasses: %d entries, %d classifiable live, absent=%s",
		len(coreNodeClasses), len(covered), strings.Join(absent, ","))
}
