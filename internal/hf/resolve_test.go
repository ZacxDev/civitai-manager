package hf

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHub serves the subset of the HF API the resolver uses, from an in-memory repo
// map, over a TLS httptest server. The resolver client is pointed at it with the
// hardened dial/redirect guards bypassed (base + a permissive http client) so the
// RESOLVER LOGIC is what's under test, not the transport.
type fakeRepo struct {
	gated     any               // false | "auto"
	private   bool
	sha       string
	downloads int
	likes     int
	license   string
	files     map[string]string // basename/path -> lfs oid (sha256)
	searchFor []string          // queries that return this repo
}

func newFakeHub(t *testing.T, repos map[string]fakeRepo) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("search")
		var out []map[string]any
		for id, repo := range repos {
			for _, sq := range repo.searchFor {
				if strings.EqualFold(sq, q) {
					out = append(out, map[string]any{
						"id": id, "downloads": repo.downloads, "likes": repo.likes,
						"private": repo.private, "gated": repo.gated,
					})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		// /api/models/{owner}/{repo}            -> model info
		// /api/models/{owner}/{repo}/tree/{rev} -> tree
		rest := strings.TrimPrefix(r.URL.Path, "/api/models/")
		if i := strings.Index(rest, "/tree/"); i >= 0 {
			repoID := rest[:i]
			repo, ok := repos[repoID]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var tree []map[string]any
			for path, oid := range repo.files {
				entry := map[string]any{"type": "file", "path": path, "size": 100}
				if oid != "" {
					entry["lfs"] = map[string]any{"oid": oid, "size": 100}
				}
				tree = append(tree, entry)
			}
			_ = json.NewEncoder(w).Encode(tree)
			return
		}
		repo, ok := repos[rest]
		if !ok {
			http.NotFound(w, r)
			return
		}
		info := map[string]any{
			"id": rest, "sha": repo.sha, "private": repo.private, "gated": repo.gated,
			"downloads": repo.downloads, "likes": repo.likes,
			"cardData": map[string]any{"license": repo.license},
		}
		_ = json.NewEncoder(w).Encode(info)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	hc := srv.Client()
	hc.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	return &Client{
		base:        srv.URL,
		httpc:       hc,
		hostOK:      func(string) bool { return true },
		tokenHostOK: func(string) bool { return false },
		denyIP:      func(net.IP) bool { return false },
	}
}

func TestResolveCuratedHit(t *testing.T) {
	const oid = "d02fe493c31e1bbc6450f4dc6f1db86a02a59322ff1f6d318da0661d72ddd084"
	c := newFakeHub(t, map[string]fakeRepo{
		"Bingsu/adetailer": {
			gated: false, sha: "53cc19de", downloads: 10700000, likes: 752, license: "apache-2.0",
			files: map[string]string{"face_yolov9c.pt": oid},
		},
	})
	m, ok, err := c.Resolve(context.Background(), "bbox/face_yolov9c.pt")
	if err != nil || !ok {
		t.Fatalf("Resolve = ok:%v err:%v, want a curated hit", ok, err)
	}
	if m.Repo != "Bingsu/adetailer" || m.FileName != "face_yolov9c.pt" {
		t.Errorf("wrong match: %+v", m)
	}
	if m.SHA256 != oid {
		t.Errorf("SHA256 = %q, want the LFS oid %q", m.SHA256, oid)
	}
	if m.Source != SourceCurated {
		t.Errorf("Source = %q, want curated", m.Source)
	}
	if m.Subdir != "ultralytics/bbox" {
		t.Errorf("Subdir = %q, want ultralytics/bbox (bbox/ prefix)", m.Subdir)
	}
	if m.Revision != "53cc19de" {
		t.Errorf("Revision = %q, want the pinned commit sha", m.Revision)
	}
	if m.Gated {
		t.Error("adetailer must be non-gated")
	}
	if !m.RecognizedOrg {
		t.Error("Bingsu is a recognized org")
	}
	if !m.AutoDownloadEligible() {
		t.Error("a curated, non-gated, exact, sha-pinned, routable match must be auto-eligible")
	}
	wantURL := "/Bingsu/adetailer/resolve/53cc19de/face_yolov9c.pt"
	if !strings.HasSuffix(m.URL, wantURL) {
		t.Errorf("URL = %q, want suffix %q", m.URL, wantURL)
	}
	// segm/ prefix routes to the segm subdir.
	m2, _, _ := c.Resolve(context.Background(), "segm/face_yolov9c.pt")
	if m2 == nil || m2.Subdir != "ultralytics/segm" {
		t.Errorf("segm/ prefix Subdir = %v, want ultralytics/segm", m2)
	}
}

func TestResolveSearchExactMatch(t *testing.T) {
	const oid = "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	c := newFakeHub(t, map[string]fakeRepo{
		// Not in the curated map; found via search; recognized org.
		"stabilityai/my-thing": {
			gated: false, sha: "abc123", downloads: 5000, likes: 10, license: "openrail",
			files:     map[string]string{"my_special_model.safetensors": oid},
			searchFor: []string{"my special model"},
		},
	})
	m, ok, err := c.Resolve(context.Background(), "my_special_model.safetensors")
	if err != nil || !ok {
		t.Fatalf("Resolve search = ok:%v err:%v", ok, err)
	}
	if m.Repo != "stabilityai/my-thing" || m.Source != SourceSearch {
		t.Errorf("wrong search match: %+v", m)
	}
	if m.SHA256 != oid {
		t.Errorf("SHA256 = %q, want %q", m.SHA256, oid)
	}
	if !m.RecognizedOrg {
		t.Error("stabilityai is a recognized org")
	}
	// No subdir can be inferred (no curated family, no bbox/segm prefix) → link-only.
	if m.Subdir != "" {
		t.Errorf("Subdir = %q, want empty (undeterminable)", m.Subdir)
	}
	if m.AutoDownloadEligible() {
		t.Error("a match with no determinable subdir must NOT be auto-eligible")
	}
}

func TestResolveNoFuzzyMatch(t *testing.T) {
	c := newFakeHub(t, map[string]fakeRepo{
		"someone/repo": {
			gated: false, sha: "x", downloads: 100,
			// A DIFFERENT basename — must NOT fuzzy-match the requested file.
			files:     map[string]string{"totally_different.safetensors": "oid"},
			searchFor: []string{"wanted model"},
		},
	})
	_, ok, err := c.Resolve(context.Background(), "wanted_model.safetensors")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Error("resolver must require an EXACT basename match — no fuzzy auto-match")
	}
}

func TestResolveGatedNotAutoEligible(t *testing.T) {
	const oid = "1111111111111111111111111111111111111111111111111111111111111111"
	c := newFakeHub(t, map[string]fakeRepo{
		"Bingsu/adetailer": {
			gated: "auto", sha: "s", downloads: 100,
			files: map[string]string{"face_yolov9c.pt": oid},
		},
	})
	m, ok, err := c.Resolve(context.Background(), "bbox/face_yolov9c.pt")
	if err != nil || !ok {
		t.Fatalf("gated repo should still resolve (for the link): ok:%v err:%v", ok, err)
	}
	if !m.Gated {
		t.Error("gated:'auto' must set Gated=true")
	}
	if m.AutoDownloadEligible() {
		t.Error("a gated repo must NEVER be auto-download eligible")
	}
}

func TestResolvePrivateRepoGated(t *testing.T) {
	c := newFakeHub(t, map[string]fakeRepo{
		"Bingsu/adetailer": {
			private: true, gated: false, sha: "s", downloads: 100,
			files: map[string]string{"face_yolov9c.pt": "2222222222222222222222222222222222222222222222222222222222222222"},
		},
	})
	m, ok, _ := c.Resolve(context.Background(), "bbox/face_yolov9c.pt")
	if !ok || !m.Gated {
		t.Fatalf("a private repo must be treated as gated: ok:%v m:%+v", ok, m)
	}
	if m.AutoDownloadEligible() {
		t.Error("a private repo must not be auto-eligible")
	}
}

func TestResolveNonLFSNoSHANotEligible(t *testing.T) {
	c := newFakeHub(t, map[string]fakeRepo{
		"Bingsu/adetailer": {
			gated: false, sha: "s", downloads: 100,
			files: map[string]string{"face_yolov9c.pt": ""}, // no LFS oid → no sha256
		},
	})
	m, ok, _ := c.Resolve(context.Background(), "bbox/face_yolov9c.pt")
	if !ok {
		t.Fatal("should still match by name")
	}
	if m.SHA256 != "" {
		t.Errorf("SHA256 = %q, want empty (no LFS block)", m.SHA256)
	}
	if m.AutoDownloadEligible() {
		t.Error("without a sha256 to pin, a match must not be auto-eligible")
	}
}

func TestResolveMiss(t *testing.T) {
	c := newFakeHub(t, map[string]fakeRepo{})
	_, ok, err := c.Resolve(context.Background(), "unknown_thing.safetensors")
	if err != nil {
		t.Fatalf("a clean miss must not error: %v", err)
	}
	if ok {
		t.Error("no repo/file → miss")
	}
}

func TestCleanQuery(t *testing.T) {
	cases := map[string]string{
		"face_yolov9c.pt":                 "face yolov9c",
		"sd_xl_base_1.0.safetensors":      "sd xl base",
		"vae-ft-mse-840000-ema-pruned.pt": "vae ft mse ema pruned", // pure-number tokens dropped
		"my_special_model.safetensors":    "my special model",
	}
	for in, want := range cases {
		if got := cleanQuery(baseName(in)); got != want {
			t.Errorf("cleanQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectorSubdir(t *testing.T) {
	cases := map[string]string{
		"bbox/face_yolov9c.pt": "ultralytics/bbox",
		"segm/person-seg.pt":   "ultralytics/segm",
		"face_yolov9c.pt":      "ultralytics/bbox", // default
		"BBOX/Face.pt":         "ultralytics/bbox", // case-insensitive
	}
	for in, want := range cases {
		if got := detectorSubdir(in); got != want {
			t.Errorf("detectorSubdir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrgRecognized(t *testing.T) {
	for _, r := range []string{"Bingsu/adetailer", "madebyollin/x", "H94/IP-Adapter"} {
		if !orgRecognized(r) {
			t.Errorf("%q owner should be recognized", r)
		}
	}
	for _, r := range []string{"randomuser/adetailer", "JCTN/adetailer"} {
		if orgRecognized(r) {
			t.Errorf("%q owner should NOT be recognized", r)
		}
	}
}
