//go:build integration

package integration

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestIntegrationModelPageData exercises the exact live read path the v0.16 model
// detail page depends on: GetModel (+ its raw body for the description),
// GetModelVersion for the latest version, and SearchImages(modelId=...) for the
// showcase gallery. It is unverified without a live token/network — it is gated
// by the `integration` build tag and CIVITAI_INTEGRATION=1.
func TestIntegrationModelPageData(t *testing.T) {
	requireIntegration(t)
	c := newClient()
	ctx := testContext(t)

	m, raw, err := c.GetModel(ctx, modelID())
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if m.ID == 0 || m.Name == "" {
		t.Fatalf("GetModel returned an empty model: %+v", m)
	}
	if len(raw) == 0 {
		t.Fatalf("GetModel returned no raw body (needed to parse the description)")
	}

	if len(m.ModelVersions) > 0 {
		vid := strconv.Itoa(m.ModelVersions[0].ID)
		if _, _, err := c.GetModelVersion(ctx, vid); err != nil {
			t.Fatalf("GetModelVersion(%s): %v", vid, err)
		}
	}

	q := url.Values{}
	q.Set("modelId", strconv.Itoa(m.ID))
	q.Set("limit", "5")
	q.Set("withMeta", "true")
	q.Set("flatMeta", "true")
	res, err := c.SearchImages(ctx, q)
	if err != nil {
		t.Fatalf("SearchImages: %v", err)
	}
	// Images may legitimately be empty for some models; just assert the shape is
	// usable when present.
	for _, im := range res.Items {
		if im.URL == "" {
			t.Errorf("image %d has no URL", im.ID)
		}
		_, _ = im.ParseMeta()
	}
	_ = civitai.MetaOK // keep the wrapper import meaningful
}
