package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// provenanceResolver builds a chip resolver whose single resource resolves to the
// given resourceInfo. openFolder is off unless a test turns it on, so these cases
// exercise the CHIP only.
func provenanceResolver(info resourceInfo, have bool) workflowResolver {
	return workflowResolver{
		haveFile:      func(string) bool { return have },
		localResource: func(string) (resourceInfo, bool) { return info, have },
	}
}

// TestResourceChipHFProvenanceLink is the table-driven pin on what a recorded
// HuggingFace provenance does — and does NOT — do to the chip.
func TestResourceChipHFProvenanceLink(t *testing.T) {
	recorded := &store.HFProvenance{
		SHA256:   "70b640f8",
		Repo:     "Bingsu/adetailer",
		Path:     "face_yolov8n.pt",
		Revision: "53cc19de382014514d9d4038601d261a7faa9b7b",
	}

	tests := []struct {
		name       string
		info       resourceInfo
		have       bool
		wantTag    string // "a" or "span"
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name:    "recorded provenance links to the PINNED revision file page",
			info:    resourceInfo{Path: "/models/ultralytics/bbox/face_yolov8n.pt", FileID: 3, HF: recorded},
			have:    true,
			wantTag: "a",
			wantSubstr: []string{
				`href="https://huggingface.co/Bingsu/adetailer/blob/53cc19de382014514d9d4038601d261a7faa9b7b/face_yolov8n.pt"`,
				`target="_blank"`,
				`rel="noopener noreferrer"`,
				`data-src="hf"`,
				"Downloaded from HuggingFace: Bingsu/adetailer/face_yolov8n.pt @ 53cc19d",
			},
			// A pinned-revision /blob/ page, never the /resolve/ raw download and
			// never a "main" branch link that would decay into a different claim.
			notSubstr: []string{"/resolve/", "/blob/main/"},
		},
		{
			name: "a CivitAI linkage WINS: exactly one link, and it is the in-app one",
			info: resourceInfo{
				Path: "/models/loras/x.safetensors", FileID: 4,
				ModelID: 7, VersionID: 8, HF: recorded,
			},
			have:       true,
			wantTag:    "a",
			wantSubstr: []string{`href="/models/7?modelVersionId=8"`},
			notSubstr:  []string{"huggingface.co", `data-src="hf"`, `target="_blank"`},
		},
		{
			name:    "NO provenance: no link and no source affordance whatsoever",
			info:    resourceInfo{Path: "/models/loras/mystery.safetensors", FileID: 5},
			have:    true,
			wantTag: "span",
			// The path is stated in the chip's detail popover. It is NOT a title= —
			// the chip owns a popover, and both on one hover unit double-paint.
			wantSubstr: []string{`data-have="yes"`,
				`<span class="cm-res-detail-value break-all">/models/loras/mystery.safetensors</span>`},
			notSubstr: []string{"href=", "huggingface.co", "data-src", "↗", "Search"},
		},
		{
			name:       "not in the library at all: no link, no provenance, no implication",
			info:       resourceInfo{},
			have:       false,
			wantTag:    "span",
			wantSubstr: []string{`data-have="no"`, "not in your library"},
			notSubstr:  []string{"href=", "huggingface.co"},
		},
		{
			name: "a provenance row with no revision degrades to the repo page",
			info: resourceInfo{
				Path: "/models/vae/x.safetensors", FileID: 6,
				HF: &store.HFProvenance{SHA256: "aa", Repo: "stabilityai/sd-vae-ft-mse", Path: "x.safetensors"},
			},
			have:       true,
			wantTag:    "a",
			wantSubstr: []string{`href="https://huggingface.co/stabilityai/sd-vae-ft-mse"`, `data-src="hf"`},
			notSubstr:  []string{"/blob/"},
		},
		{
			name: "a provenance row with no repo yields NO link at all",
			info: resourceInfo{
				Path: "/models/vae/x.safetensors", FileID: 7,
				HF: &store.HFProvenance{SHA256: "aa", Path: "x.safetensors", Revision: "abc"},
			},
			have:      true,
			wantTag:   "span",
			notSubstr: []string{"href=", "huggingface.co"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderString(t, workflowResourceChip("face_yolov8n.pt", provenanceResolver(tc.info, tc.have)))
			// 🔴 Name the CHIP ELEMENT. The chip is wrapped together with its detail
			// popover, so the old HasPrefix no longer identifies it — and relaxing this
			// to strings.Contains(got, "<a ") is precisely the vacuity the audit caught:
			// any <a> anywhere in the fragment satisfied it, so a chip that stopped
			// being a link at all kept the suite green.
			if tag := resChipTag(t, got); !strings.HasPrefix(tag, "<"+tc.wantTag+" ") {
				t.Fatalf("expected a <%s> chip, its opening tag is:\n%s\nin:\n%s", tc.wantTag, tag, got)
			}
			// EXACTLY ONE href per chip, and it is the chip's own. This is the real
			// invariant, restored rather than deleted: the detail popover deliberately
			// emits no links, so a "View model" row duplicating the chip's destination
			// (or an off-site HF link beside an in-app CivitAI one) fails here.
			if n := strings.Count(got, "href="); tc.wantTag == "a" && n != 1 {
				t.Fatalf("expected exactly one href, got %d:\n%s", n, got)
			}
			if n := strings.Count(got, "href="); tc.wantTag == "span" && n != 0 {
				t.Fatalf("a non-linked chip must carry no href at all, got %d:\n%s", n, got)
			}
			for _, w := range tc.wantSubstr {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, n := range tc.notSubstr {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in:\n%s", n, got)
				}
			}
		})
	}
}

// TestResourceChipHFHrefIsAlwaysSafe: a hostile repo/path must render escaped,
// must never break out of the attribute, and must never produce a non-http href.
func TestResourceChipHFHrefIsAlwaysSafe(t *testing.T) {
	tests := []struct {
		name string
		p    store.HFProvenance
		// wantHref is "" when the chip must carry no href at all.
		wantHref string
	}{
		{
			name:     "quotes and angle brackets in the repo id are escaped, not injected",
			p:        store.HFProvenance{SHA256: "a", Repo: `x"><script>alert(1)</script>`, Path: "f.pt", Revision: "r1"},
			wantHref: "https://huggingface.co/x%22%3E%3Cscript%3Ealert%281%29%3C/script%3E/blob/r1/f.pt",
		},
		{
			name:     "a query/fragment in the path cannot truncate the URL",
			p:        store.HFProvenance{SHA256: "a", Repo: "org/repo", Path: "d/x?a=1#z.pt", Revision: "r1"},
			wantHref: "https://huggingface.co/org/repo/blob/r1/d/x%3Fa=1%23z.pt",
		},
		{
			name:     "a javascript: repo id cannot repoint the origin",
			p:        store.HFProvenance{SHA256: "a", Repo: "javascript:alert(1)", Path: "f.pt", Revision: "r1"},
			wantHref: "https://huggingface.co/javascript:alert%281%29/blob/r1/f.pt",
		},
		{
			name:     "traversal segments stay on huggingface.co",
			p:        store.HFProvenance{SHA256: "a", Repo: "../../evil.example.com", Path: "f.pt", Revision: "r1"},
			wantHref: "https://huggingface.co/../../evil.example.com/blob/r1/f.pt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := resourceInfo{Path: "/lib/f.pt", FileID: 1, HF: &tc.p}
			got := renderString(t, workflowResourceChip("f.pt", provenanceResolver(info, true)))

			if tc.wantHref == "" {
				if strings.Contains(got, "href=") {
					t.Fatalf("expected no href, got:\n%s", got)
				}
				return
			}
			if !strings.Contains(got, `href="`+tc.wantHref+`"`) {
				t.Fatalf("expected href %q in:\n%s", tc.wantHref, got)
			}
			// Whatever the row contained, nothing may open a tag or escape an
			// attribute, and the href must still be a safe http(s) URL.
			for _, bad := range []string{"<script", "javascript:alert(1)\"", "onerror="} {
				if strings.Contains(got, bad) {
					t.Fatalf("hostile provenance leaked (%q):\n%s", bad, got)
				}
			}
			if !isSafeHTTPURL(tc.wantHref) {
				t.Fatalf("an href that is not a safe http URL was rendered: %q", tc.wantHref)
			}
		})
	}
}

// TestResourceChipNonHTTPProvenanceNeverBecomesHref pins hfHref's contract
// directly: only an http(s) URL may become an href, and a row that cannot produce
// one produces NO link rather than a broken one.
func TestResourceChipNonHTTPProvenanceNeverBecomesHref(t *testing.T) {
	tests := []struct {
		name   string
		info   resourceInfo
		wantOK bool
	}{
		{"no provenance at all", resourceInfo{}, false},
		{"blank repo and blank path", resourceInfo{HF: &store.HFProvenance{SHA256: "a"}}, false},
		{"repo only degrades to the repo page", resourceInfo{HF: &store.HFProvenance{SHA256: "a", Repo: "o/r"}}, true},
		{"complete row", resourceInfo{HF: &store.HFProvenance{SHA256: "a", Repo: "o/r", Path: "f", Revision: "v"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			href, ok := tc.info.hfHref()
			if ok != tc.wantOK {
				t.Fatalf("hfHref() ok = %v, want %v (href=%q)", ok, tc.wantOK, href)
			}
			if ok && !isSafeHTTPURL(href) {
				t.Fatalf("hfHref() returned a non-http URL: %q", href)
			}
			if !ok && href != "" {
				t.Fatalf("hfHref() returned %q with ok=false", href)
			}
		})
	}
}

// TestLocalResourceReadsProvenanceFromTheStore closes the loop end-to-end through
// the PRODUCTION resolver: a recorded row plus an indexed file makes the detail
// page's chip carry the pinned-revision link, and removing the row removes the
// link entirely.
func TestLocalResourceReadsProvenanceFromTheStore(t *testing.T) {
	srv := newWorkflowServer(t)
	const sha = "70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c"

	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/models/ultralytics/bbox/face_yolov8n.pt", SHA256: sha, SizeBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}

	// No provenance yet: no link.
	chip := renderString(t, workflowResourceChip("face_yolov8n.pt", srv.workflowResolver()))
	if strings.Contains(chip, "huggingface.co") {
		t.Fatalf("a file with no recorded provenance must not link anywhere:\n%s", chip)
	}

	if err := srv.store.UpsertHFProvenance(store.HFProvenance{
		SHA256: sha, Repo: "Bingsu/adetailer", Path: "face_yolov8n.pt", Revision: "53cc19de",
	}); err != nil {
		t.Fatal(err)
	}
	chip = renderString(t, workflowResourceChip("face_yolov8n.pt", srv.workflowResolver()))
	want := `href="https://huggingface.co/Bingsu/adetailer/blob/53cc19de/face_yolov8n.pt"`
	if !strings.Contains(chip, want) {
		t.Fatalf("expected %s in:\n%s", want, chip)
	}
}
