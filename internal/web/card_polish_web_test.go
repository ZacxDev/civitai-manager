package web

import (
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// TestCardWorkflowsBadgeOmitted proves the redundant "Workflows" type chip is
// dropped on workflow cards while a normal model card keeps its type badge.
func TestCardWorkflowsBadgeOmitted(t *testing.T) {
	wf := civitai.ModelListItem{ID: 1, Name: "A Workflow", Type: "Workflows"}
	out := renderString(t, modelCardCore(wf, nil, "show", modelUpdateInfo{}, nil))
	if strings.Contains(out, ">Workflows<") {
		t.Errorf("workflow card should NOT render a 'Workflows' type badge:\n%s", out)
	}

	ckpt := civitai.ModelListItem{ID: 2, Name: "A Checkpoint", Type: "Checkpoint"}
	out = renderString(t, modelCardCore(ckpt, nil, "show", modelUpdateInfo{}, nil))
	if !strings.Contains(out, ">Checkpoint<") {
		t.Errorf("model card should still render its 'Checkpoint' type badge:\n%s", out)
	}
}

// TestCardUpdatedLineRendersLast proves the "Updated X ago" line renders as the
// LAST element of the card — after the stats row AND after the primary action —
// so it sits bottom-left, while keeping its hover popover markup.
func TestCardUpdatedLineRendersLast(t *testing.T) {
	it := civitai.ModelListItem{ID: 7, Name: "Ordered", Type: "LORA",
		Stats: civitai.ModelStats{DownloadCount: 10, ThumbsUpCount: 5}}
	updated := modelUpdateInfo{At: time.Now().Add(-72 * time.Hour), Name: "v2", VersionID: 9}
	action := h.Button(g.Text("ACTIONMARKER"))
	out := renderString(t, modelCardCore(it, nil, "show", updated, action))

	iStats := strings.Index(out, "cm-stats")
	iAction := strings.Index(out, "ACTIONMARKER")
	iUpdated := strings.Index(out, "cm-updated")
	if iStats < 0 || iAction < 0 || iUpdated < 0 {
		t.Fatalf("missing markers (stats=%d action=%d updated=%d):\n%s", iStats, iAction, iUpdated, out)
	}
	if !(iStats < iAction && iAction < iUpdated) {
		t.Errorf("updated line must be last (want stats<action<updated, got %d,%d,%d):\n%s",
			iStats, iAction, iUpdated, out)
	}
	// The hover popover markup is preserved.
	if !strings.Contains(out, "cm-updated-pop") {
		t.Errorf("updated line should keep its popover markup (cm-updated-pop):\n%s", out)
	}
}
