package comfy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// realVHSHistoryBody is a VERBATIM copy of a real /history entry from the user's
// live ComfyUI 0.27.1 (prompt 7f6774d3-…, node 70, a VHS_VideoCombine save),
// captured 2026-07-31. It is copied rather than synthesized on purpose: every
// fake-reader assumption in this repo that was invented rather than observed has
// eventually turned out to be wrong about the real upstream.
//
// Note what it does NOT contain: an `images` key. That is the whole bug — the
// harvest read `images` only, so this entry yielded zero refs, captureGeneration
// returned early on len(res.Images)==0, and NO generation row was ever written.
const realVHSHistoryBody = `{
  "outputs": {
    "70": {
      "gifs": [
        {
          "filename": "base_00245.mp4",
          "subfolder": "video/wan22/base",
          "type": "output",
          "format": "video/h264-mp4",
          "frame_rate": 30.0,
          "workflow": "base_00245.png",
          "fullpath": "/home/zach/workspace/fast/comfyui/ComfyUI/output/video/wan22/base/base_00245.mp4"
        }
      ]
    }
  },
  "status": {"completed": true, "status_str": "success"}
}`

// TestAllImagesHarvestsRealVHSGifsEntry is the regression test for the reported
// bug, driven by the real payload above.
func TestAllImagesHarvestsRealVHSGifsEntry(t *testing.T) {
	var entry HistoryEntry
	if err := json.Unmarshal([]byte(realVHSHistoryBody), &entry); err != nil {
		t.Fatalf("decode real VHS history: %v", err)
	}

	refs := entry.AllImages()
	if len(refs) != 1 {
		t.Fatalf("a gifs-only VHS entry must yield exactly 1 ref, got %d (%+v) — "+
			"this is the bug: harvesting only `images` captures NOTHING for a "+
			"video-only workflow", len(refs), refs)
	}
	got := refs[0]
	if got.Filename != "base_00245.mp4" {
		t.Errorf("filename = %q, want base_00245.mp4", got.Filename)
	}
	if got.Subfolder != "video/wan22/base" {
		t.Errorf("subfolder = %q, want video/wan22/base", got.Subfolder)
	}
	if got.Type != "output" {
		t.Errorf("type = %q, want output", got.Type)
	}
	// The bogus format string must survive decode UNCHANGED. Sanitizing it here
	// would hide the trap from outputMediaType, whose whole job is refusing it.
	if got.Format != "video/h264-mp4" {
		t.Errorf("format = %q, want the raw upstream video/h264-mp4", got.Format)
	}
}

// TestImageRefNeverDecodesFullpath asserts the `fullpath` absolute path from the
// untrusted upstream is not reachable through ImageRef AT ALL.
//
// It is a structural guard, not a behavioural one: the strongest way to guarantee
// nothing ever reads an attacker-chosen absolute path is for the field not to
// exist. If someone adds it "for logging", this fails and they have to come read
// the comment on ImageRef.
//
// 🔴 IT REFLECTS OVER THE STRUCT TYPE, and it does so because the obvious version
// DID NOT WORK. Marshalling a zero-valued ImageRef and checking the resulting keys
// looks equivalent and is not: `omitempty` on a newly-added `Fullpath` drops it
// from the output, so the round-trip check stayed GREEN through a deliberate
// mutation that added the field AND made captureGeneration read it. Reflection sees
// the field regardless of tag options and regardless of value.
func TestImageRefNeverDecodesFullpath(t *testing.T) {
	banned := map[string]string{
		"fullpath": "an absolute path on the ComfyUI host — reading it would let an " +
			"untrusted upstream name any file on this machine",
		"workflow": "the companion PNG's filename — fetch it through /view like any " +
			"other output if it is ever needed, do not treat it as a path",
		"frame_rate": "unused; decoding it invites treating VHS metadata as trusted",
	}

	rt := reflect.TypeOf(ImageRef{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		key := strings.ToLower(tag)
		if key == "" || key == "-" {
			key = strings.ToLower(f.Name)
		}
		if why, bad := banned[key]; bad {
			t.Errorf("ImageRef.%s decodes %q — %s", f.Name, key, why)
		}
	}

	// The behavioural half: decoding the REAL payload must not surface those keys
	// anywhere, i.e. the fields really are absent rather than merely untagged.
	var ref ImageRef
	if err := json.Unmarshal([]byte(`{"filename":"a.mp4","subfolder":"s","type":"output",
		"format":"video/h264-mp4","workflow":"a.png","frame_rate":30.0,
		"fullpath":"/etc/shadow"}`), &ref); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	blob, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for key := range banned {
		if strings.Contains(strings.ToLower(string(blob)), key) {
			t.Errorf("re-marshalled ImageRef carries %q: %s", key, blob)
		}
	}
	if strings.Contains(string(blob), "/etc/shadow") {
		t.Errorf("the untrusted absolute path survived the decode: %s", blob)
	}
}

// TestAllImagesImagesOnlyIsUnchanged is the regression guard for the ONLY thing
// that worked before this change. A pure-`images` entry must decode byte-identically
// to how it always did.
func TestAllImagesImagesOnlyIsUnchanged(t *testing.T) {
	const body = `{"outputs":{"9":{"images":[
		{"filename":"a.png","subfolder":"","type":"output"},
		{"filename":"b.png","subfolder":"sub","type":"temp"}]}}}`
	var entry HistoryEntry
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refs := entry.AllImages()
	want := []ImageRef{
		{Filename: "a.png", Subfolder: "", Type: "output"},
		{Filename: "b.png", Subfolder: "sub", Type: "temp"},
	}
	if len(refs) != len(want) {
		t.Fatalf("got %d refs, want %d (%+v)", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref[%d] = %+v, want %+v", i, refs[i], want[i])
		}
	}
}

// TestAllImagesCapturesBothKindsFromOneNode covers a node emitting BOTH — the real
// shape of the user's workflow 570 (a PreviewImage beside a VHS_VideoCombine).
// Within a node, images come first, then gifs.
func TestAllImagesCapturesBothKindsFromOneNode(t *testing.T) {
	const body = `{"outputs":{"70":{
		"images":[{"filename":"preview.png","subfolder":"","type":"output"}],
		"gifs":[{"filename":"clip.mp4","subfolder":"v","type":"output","format":"video/h264-mp4"}]}}}`
	var entry HistoryEntry
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refs := entry.AllImages()
	if len(refs) != 2 {
		t.Fatalf("a node emitting both kinds must yield 2 refs, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Filename != "preview.png" || refs[1].Filename != "clip.mp4" {
		t.Errorf("order = [%s %s], want [preview.png clip.mp4] (images before gifs)",
			refs[0].Filename, refs[1].Filename)
	}
}

// TestAllImagesIsDeterministicAcrossNodes pins the node ordering.
//
// 🔴 This is a REAL bug the video work uncovered, not a hypothetical: AllImages
// documented "node-key-sorted order" while ranging over a Go map, whose iteration
// order is randomized per run. The captured idx — which is PERSISTED and decides
// which output becomes the gallery thumbnail — therefore differed between runs of
// the same prompt whenever a graph had two output nodes. Video makes this reachable
// for the user's library (a VHS node + a PreviewImage node in one graph).
//
// Node "9" must sort BEFORE "70": numerically, not lexically. A plain sort.Strings
// would order "70" first and pass a weaker version of this test.
func TestAllImagesIsDeterministicAcrossNodes(t *testing.T) {
	const body = `{"outputs":{
		"70":{"gifs":[{"filename":"clip.mp4","type":"output"}]},
		"9":{"images":[{"filename":"a.png","type":"output"}]},
		"12":{"images":[{"filename":"b.png","type":"output"}]}}}`

	// Decode + harvest repeatedly: one pass could get lucky with the map order.
	for i := 0; i < 50; i++ {
		var entry HistoryEntry
		if err := json.Unmarshal([]byte(body), &entry); err != nil {
			t.Fatalf("decode: %v", err)
		}
		refs := entry.AllImages()
		if len(refs) != 3 {
			t.Fatalf("got %d refs, want 3", len(refs))
		}
		got := []string{refs[0].Filename, refs[1].Filename, refs[2].Filename}
		want := []string{"a.png", "b.png", "clip.mp4"} // nodes 9, 12, 70
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: order = %v, want %v (nodes must sort 9,12,70 "+
					"NUMERICALLY — a map range or a lexical sort fails here)", i, got, want)
			}
		}
	}
}
