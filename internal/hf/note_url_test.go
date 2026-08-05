package hf

import (
	"context"
	"testing"
)

func TestParseResolveURLAccepts(t *testing.T) {
	cases := []struct {
		raw                     string
		repo, revision, subPath string
	}{
		{
			// The real note URL from the operator's workflow 590.
			raw:      "https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors",
			repo:     "F16/z-image-turbo-sda",
			revision: "main",
			subPath:  "zit_sda_v1.safetensors",
		},
		{
			// Also from wf590 — a file nested several segments deep.
			raw:      "https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors",
			repo:     "Comfy-Org/z_image_turbo",
			revision: "main",
			subPath:  "split_files/vae/ae.safetensors",
		},
		{
			raw:      "https://huggingface.co/o/r/resolve/53cc19de/a.safetensors?download=true",
			repo:     "o/r",
			revision: "53cc19de",
			subPath:  "a.safetensors",
		},
		{
			raw:      "https://HuggingFace.CO/o/r/resolve/main/a.safetensors",
			repo:     "o/r",
			revision: "main",
			subPath:  "a.safetensors",
		},
		{
			raw:      "https://huggingface.co/o/r/resolve/main/my%20model.safetensors",
			repo:     "o/r",
			revision: "main",
			subPath:  "my model.safetensors",
		},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			repo, rev, p, ok := ParseResolveURL(tc.raw)
			if !ok {
				t.Fatalf("ParseResolveURL(%q) = not ok", tc.raw)
			}
			if repo != tc.repo || rev != tc.revision || p != tc.subPath {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)", repo, rev, p, tc.repo, tc.revision, tc.subPath)
			}
		})
	}
}

func TestParseResolveURLRefuses(t *testing.T) {
	cases := map[string]string{
		"another host entirely":       "https://github.com/o/r/resolve/main/a.safetensors",
		"a lookalike host":            "https://huggingface.co.evil.com/o/r/resolve/main/a.safetensors",
		"a suffix lookalike host":     "https://evilhuggingface.co/o/r/resolve/main/a.safetensors",
		"a subdomain":                 "https://cdn-lfs.huggingface.co/o/r/resolve/main/a.safetensors",
		"the CDN allowlisted for GET": "https://us.aws.cdn.hf.co/o/r/resolve/main/a.safetensors",
		"http":                        "http://huggingface.co/o/r/resolve/main/a.safetensors",
		"no scheme":                   "huggingface.co/o/r/resolve/main/a.safetensors",
		"the repo page":               "https://huggingface.co/F16/z-image-turbo-sda",
		"the blob viewer":             "https://huggingface.co/o/r/blob/main/a.safetensors",
		"tree browsing":               "https://huggingface.co/o/r/tree/main",
		"no file path":                "https://huggingface.co/o/r/resolve/main/",
		"missing revision":            "https://huggingface.co/o/r/resolve//a.safetensors",
		"a traversal segment":         "https://huggingface.co/o/r/resolve/main/..%2Fetc%2Fpasswd",
		"an escaped separator":        "https://huggingface.co/o/r/resolve/ma%2Fin/a.safetensors",
		"empty":                       "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if repo, rev, p, ok := ParseResolveURL(raw); ok {
				t.Fatalf("ParseResolveURL(%q) accepted it as (%q,%q,%q)", raw, repo, rev, p)
			}
		})
	}
}

func TestResolveInRepoPinsTheRevisionAndTheHash(t *testing.T) {
	const oid = "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	c := newFakeHub(t, map[string]fakeRepo{
		// A repo NOT in the curated map and NOT a recognized org — exactly the wf590
		// case that Resolve's search half cannot reach.
		"F16/z-image-turbo-sda": {
			gated: false, sha: "0af71d2c", downloads: 42, license: "apache-2.0",
			files: map[string]string{"zit_sda_v1.safetensors": oid},
		},
	})

	// Negative control FIRST: the ordinary resolver genuinely cannot find this file
	// (the repo name shares nothing with the filename), which is why the note path
	// exists at all.
	if _, ok, err := c.Resolve(context.Background(), "zit_sda_v1.safetensors"); ok || err != nil {
		t.Fatalf("Resolve found it (ok=%v err=%v) — the premise of this path is gone", ok, err)
	}

	m, ok, err := c.ResolveInRepo(context.Background(), "F16/z-image-turbo-sda", "zit_sda_v1.safetensors")
	if err != nil || !ok {
		t.Fatalf("ResolveInRepo = ok:%v err:%v, want a hit", ok, err)
	}
	if m.SHA256 != oid {
		t.Errorf("SHA256 = %q, want the LFS oid %q", m.SHA256, oid)
	}
	if m.Revision != "0af71d2c" {
		t.Errorf("Revision = %q, want the repo's commit sha — a note URL says main, which moves", m.Revision)
	}
	if m.Source != SourceNote {
		t.Errorf("Source = %q, want %q", m.Source, SourceNote)
	}
	if m.RecognizedOrg {
		t.Error("F16 is not a recognized org and must not be reported as one")
	}
	if m.Subdir != "" {
		t.Errorf("Subdir = %q, want empty — the destination comes from the workflow's model type", m.Subdir)
	}
	// 🔴 A note match must never satisfy the curated set's auto-download predicate.
	if m.AutoDownloadEligible() {
		t.Error("a SourceNote match must not be AutoDownloadEligible")
	}
}

// 🔴 The exclusion must hold in the WORST case, not the incidental one. The audit
// found the shipped comment false: `RecognizedOrg` is set from the repo OWNER, and
// an untrusted author can name `stabilityai/...` as easily as anyone, so the second
// arm of AutoDownloadEligible was satisfied and only the empty Subdir was holding
// the gate. This builds exactly the state that used to slip through — a RECOGNIZED
// org, a populated Subdir, a real SHA256, un-gated — and asserts it is still
// ineligible.
func TestNoteMatchIsNeverAutoDownloadEligible(t *testing.T) {
	const oid = "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	c := newFakeHub(t, map[string]fakeRepo{
		// A RECOGNIZED org — the case that used to slip through.
		"stabilityai/sd-turbo": {
			gated: false, sha: "abc123", downloads: 999,
			files: map[string]string{"sd_turbo.safetensors": oid},
		},
	})
	m, ok, err := c.ResolveInRepo(context.Background(), "stabilityai/sd-turbo", "sd_turbo.safetensors")
	if err != nil || !ok {
		t.Fatalf("ResolveInRepo = ok:%v err:%v", ok, err)
	}
	// PRECONDITIONS: the fixture really does reach the dangerous state. Without
	// these, an ineligible verdict below could come from a missing hash or an
	// unrecognised owner and would prove nothing about the note exclusion.
	if !m.RecognizedOrg {
		t.Fatal("precondition: stabilityai must be a recognized org, or this test cannot reach the case")
	}
	if m.SHA256 == "" || m.Gated {
		t.Fatalf("precondition: want a hashed, un-gated match, got sha=%q gated=%v", m.SHA256, m.Gated)
	}
	if m.Source != SourceNote {
		t.Fatalf("precondition: Source = %q, want %q", m.Source, SourceNote)
	}

	// The subdir is the one thing the note install path computes for itself
	// (comfy.TypeSubdir), so populating it here is not hypothetical — it is what a
	// plausible edit to this package would do.
	m.Subdir = "checkpoints"
	if m.AutoDownloadEligible() {
		t.Fatal("a SourceNote match in a recognized org, with a subdir and a sha, reported " +
			"AutoDownloadEligible — the note path must never inherit the resolver's authority")
	}

	// POSITIVE CONTROL: the SAME match with the source flipped to curated IS
	// eligible, so the refusal above is a fact about SourceNote and not about some
	// other field this fixture happens to leave unsatisfied.
	m.Source = SourceCurated
	if !m.AutoDownloadEligible() {
		t.Fatal("positive control: an otherwise identical curated match must be eligible — " +
			"the refusal above is not attributable to the source")
	}
}

func TestResolveInRepoMissesAndGates(t *testing.T) {
	const oid = "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	c := newFakeHub(t, map[string]fakeRepo{
		"o/open":   {gated: false, sha: "aaaa", files: map[string]string{"a.safetensors": oid}},
		"o/gated":  {gated: "manual", sha: "bbbb", files: map[string]string{"g.safetensors": oid}},
		"o/nolfs":  {gated: false, sha: "cccc", files: map[string]string{"small.pt": ""}},
		"o/nested": {gated: false, sha: "dddd", files: map[string]string{"sub/dir/n.safetensors": oid}},
	})
	ctx := context.Background()

	t.Run("a file the repo does not have is a clean miss", func(t *testing.T) {
		if _, ok, err := c.ResolveInRepo(ctx, "o/open", "absent.safetensors"); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want a clean miss", ok, err)
		}
	})
	t.Run("an unknown repo is a miss, not a crash", func(t *testing.T) {
		_, ok, _ := c.ResolveInRepo(ctx, "o/nope", "a.safetensors")
		if ok {
			t.Fatal("resolved a repo that does not exist")
		}
	})
	t.Run("a gated repo still resolves but is flagged", func(t *testing.T) {
		m, ok, err := c.ResolveInRepo(ctx, "o/gated", "g.safetensors")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if !m.Gated {
			t.Fatal("Gated = false for a gated:manual repo")
		}
	})
	t.Run("a non-LFS file carries no sha to pin", func(t *testing.T) {
		m, ok, err := c.ResolveInRepo(ctx, "o/nolfs", "small.pt")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if m.SHA256 != "" {
			t.Fatalf("SHA256 = %q, want empty (the tree exposes only a git blob sha1)", m.SHA256)
		}
	})
	t.Run("matching is on the basename inside the repo tree", func(t *testing.T) {
		m, ok, err := c.ResolveInRepo(ctx, "o/nested", "n.safetensors")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if m.Path != "sub/dir/n.safetensors" {
			t.Fatalf("Path = %q, want the full in-repo path", m.Path)
		}
	})
	t.Run("empty and traversal basenames resolve nothing", func(t *testing.T) {
		for _, b := range []string{"", "  ", ".", ".."} {
			if _, ok, _ := c.ResolveInRepo(ctx, "o/open", b); ok {
				t.Fatalf("basename %q resolved", b)
			}
		}
		if _, ok, _ := c.ResolveInRepo(ctx, "", "a.safetensors"); ok {
			t.Fatal("empty repo resolved")
		}
	})
}
