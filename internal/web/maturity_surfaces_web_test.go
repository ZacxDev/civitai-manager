package web

import (
	"strconv"
	"strings"
	"testing"
)

// 🔴 SURFACES WITH NO MATURITY LEVEL MUST ALWAYS RENDER.
//
// The outputs gallery, the per-batch gallery and the recent-outputs rail all show
// the user's OWN local generations. Nobody has rated them — they carry no
// browsingLevel, no nsfwLevel, nothing.
//
// "No level" here is NOT maturityUnknown. That sentinel is the fail-CLOSED answer
// for CivitAI-sourced content whose rating we expected and did not get (a garbage
// nsfwLevel, a Blocked bucket, an absent field) — such content is omitted. The
// user's own outputs are a different thing entirely: they are OUT OF SCOPE of a
// scale that describes CivitAI material, so the range never reaches them.
//
// Treating the two the same would silently blank the user's own work at anything
// narrower than the full range, and — because the range defaults to full — the bug
// would only surface for the users who narrowed it.
func TestOutputSurfacesAreNeverFilteredByMaturity(t *testing.T) {
	for _, rng := range []string{"pg:xxx", "pg:pg", "r:r", "x:x", "xxx:xxx"} {
		t.Run(rng, func(t *testing.T) {
			srv, root := newOutputsServer(t, "127.0.0.1:8787")
			wf := seedWF(t, srv, "wf")
			genID, _ := seedGen(t, srv, root, &wf, "wf", []byte("X"))
			if _, ok := parseMaturityRange(rng); !ok {
				t.Fatalf("fixture range %q is not valid — the case would assert nothing", rng)
			}
			if err := srv.store.SetSetting(maturitySettingKey, rng); err != nil {
				t.Fatal(err)
			}

			// The gallery.
			gallery := get(t, srv, "/outputs").Body.String()
			if !strings.Contains(gallery, "/outputs/img/") {
				t.Errorf("range %s emptied the outputs gallery — the user's own generations "+
					"carry no maturity level and must never be filtered:\n%s", rng, gallery)
			}
			if !strings.Contains(gallery, "/outputs/"+strconv.FormatInt(genID, 10)) {
				t.Errorf("range %s dropped the gallery tile's detail link", rng)
			}

			// The per-output detail page.
			detail := get(t, srv, "/outputs/"+strconv.FormatInt(genID, 10)).Body.String()
			if !strings.Contains(detail, "/outputs/img/") {
				t.Errorf("range %s emptied the output detail page", rng)
			}

			// And nothing anywhere is obscured client-side — blur is gone app-wide.
			for _, dead := range []string{"cm-blur", "data-blurred", "cmReveal", `data-nsfw="blur"`} {
				if strings.Contains(gallery, dead) || strings.Contains(detail, dead) {
					t.Errorf("range %s emitted the dead blur marker %q", rng, dead)
				}
			}
		})
	}
}

// TestBatchGalleryIsNeverFilteredByMaturity is the same rule for the per-batch
// gallery, which is a separate renderer and could drift.
func TestBatchGalleryIsNeverFilteredByMaturity(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	seedBatch(t, srv, root, "batch-maturity", "Preset", 2, 2)
	const batch = "batch-maturity"

	for _, rng := range []string{"pg:xxx", "pg:pg", "xxx:xxx"} {
		if err := srv.store.SetSetting(maturitySettingKey, rng); err != nil {
			t.Fatal(err)
		}
		body := get(t, srv, "/outputs/batch/"+batch).Body.String()
		if !strings.Contains(body, "/outputs/img/") {
			t.Errorf("range %s emptied the batch gallery:\n%s", rng, body)
		}
		for _, dead := range []string{"cm-blur", "data-blurred", "cmReveal"} {
			if strings.Contains(body, dead) {
				t.Errorf("range %s emitted the dead blur marker %q", rng, dead)
			}
		}
	}
}
