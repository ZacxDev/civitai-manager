package comfy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// uiNoteGraph builds a minimal UI-format graph out of (id, type, text) triples.
func uiNoteGraph(t *testing.T, nodes ...[3]string) json.RawMessage {
	t.Helper()
	var parts []string
	for _, n := range nodes {
		txt, err := json.Marshal(n[2])
		if err != nil {
			t.Fatalf("marshal note text: %v", err)
		}
		parts = append(parts, `{"id":`+n[0]+`,"type":"`+n[1]+`","widgets_values":[`+string(txt)+`]}`)
	}
	return json.RawMessage(`{"nodes":[` + strings.Join(parts, ",") + `],"links":[]}`)
}

func urlsOf(links []NoteLink) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.URL)
	}
	return out
}

func TestExtractNoteLinksShapes(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		text string
		want []string
	}{
		{
			name: "bare url in a Note",
			typ:  "Note",
			text: "grab it here https://huggingface.co/o/r/resolve/main/a.safetensors before running",
			want: []string{"https://huggingface.co/o/r/resolve/main/a.safetensors"},
		},
		{
			name: "markdown link in a MarkdownNote",
			typ:  "MarkdownNote",
			text: "- [a.safetensors](https://huggingface.co/o/r/resolve/main/a.safetensors)",
			want: []string{"https://huggingface.co/o/r/resolve/main/a.safetensors"},
		},
		{
			name: "several urls in one note keep document order",
			typ:  "MarkdownNote",
			text: "first https://example.com/one.safetensors\nsecond https://example.com/two.pth\n",
			want: []string{"https://example.com/one.safetensors", "https://example.com/two.pth"},
		},
		{
			name: "http is rejected",
			typ:  "MarkdownNote",
			text: "http://huggingface.co/o/r/resolve/main/a.safetensors",
			want: nil,
		},
		{
			name: "https inside a longer http-only sentence is still rejected",
			typ:  "Note",
			text: "mirror at http://example.com/a.safetensors (no tls)",
			want: nil,
		},
		{
			name: "prose with no url yields nothing",
			typ:  "MarkdownNote",
			text: "Change denoise up to 0.54. See models/loras. 全线MOODY系列模型均推荐使用ZIB模型！",
			want: nil,
		},
		{
			name: "trailing sentence punctuation is trimmed",
			typ:  "Note",
			text: "download https://example.com/a.safetensors.",
			want: []string{"https://example.com/a.safetensors"},
		},
		{
			name: "a markdown table cell terminates the url",
			typ:  "MarkdownNote",
			text: "| file | https://example.com/a.safetensors | notes |",
			want: []string{"https://example.com/a.safetensors"},
		},
		{
			name: "duplicate urls are kept once",
			typ:  "MarkdownNote",
			text: "[a](https://example.com/a.safetensors) and again https://example.com/a.safetensors",
			want: []string{"https://example.com/a.safetensors"},
		},
		{
			name: "a non-note node's text is ignored",
			typ:  "CheckpointLoaderSimple",
			text: "https://example.com/a.safetensors",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := urlsOf(ExtractNoteLinks(FormatUI, uiNoteGraph(t, [3]string{"7", tc.typ, tc.text})))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("index %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The mode decision is documented in notes.go: a note never executes, so the
// converter's "a bypassed node was never going to run" rule does not transfer.
// A bypassed note is still readable documentation and its links are still offered.
func TestExtractNoteLinksIncludesBypassedNotes(t *testing.T) {
	graph := json.RawMessage(`{"nodes":[
	  {"id":1,"type":"MarkdownNote","mode":4,"widgets_values":["https://example.com/bypassed.safetensors"]},
	  {"id":2,"type":"Note","mode":2,"widgets_values":["https://example.com/muted.safetensors"]},
	  {"id":3,"type":"MarkdownNote","mode":0,"widgets_values":["https://example.com/active.safetensors"]}
	]}`)
	got := urlsOf(ExtractNoteLinks(FormatUI, graph))
	want := []string{
		"https://example.com/bypassed.safetensors",
		"https://example.com/muted.safetensors",
		"https://example.com/active.safetensors",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// 🔴 The api graph has NO notes — conversion drops them — so asking for them there
// must return nothing rather than appear to work.
func TestExtractNoteLinksRefusesNonUIFormats(t *testing.T) {
	ui := uiNoteGraph(t, [3]string{"1", "MarkdownNote", "https://example.com/a.safetensors"})
	// Positive control FIRST: the same bytes DO yield a link under FormatUI, so a
	// nil below is a fact about the format gate and not about the fixture.
	if n := len(ExtractNoteLinks(FormatUI, ui)); n != 1 {
		t.Fatalf("positive control: FormatUI yielded %d links, want 1", n)
	}
	for _, format := range []string{FormatAPI, "", "ui ", "UI"} {
		if got := ExtractNoteLinks(format, ui); got != nil {
			t.Fatalf("format %q: got %v, want nil", format, got)
		}
	}
}

func TestExtractNoteLinksMalformedGraphYieldsNothing(t *testing.T) {
	cases := map[string]string{
		"truncated json":      `{"nodes":[{"id":1,`,
		"nodes is not array":  `{"nodes":{"a":1}}`,
		"empty document":      ``,
		"json null":           `null`,
		"widgets_values null": `{"nodes":[{"id":1,"type":"Note","widgets_values":null}]}`,
		"widgets_values num":  `{"nodes":[{"id":1,"type":"Note","widgets_values":[1,2,3]}]}`,
		"no nodes key":        `{"links":[]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ExtractNoteLinks(FormatUI, json.RawMessage(raw)); got != nil {
				t.Fatalf("got %v, want nil", got)
			}
		})
	}
}

// 🔴 EVERY CAP IS PINNED BY A LITERAL, never by the constant it measures.
//
// The whole point of a bound is the NUMBER, and a fixture sized as
// `(noteMaxTextBytes/32)+64` moves with the constant so no change to it can ever
// fail. Measured on the first version of these tests: raising noteMaxTextBytes
// 100x left internal/comfy at 592 PASS / 0 FAIL and internal/web at 2042 PASS /
// 0 FAIL, and at 1 GiB it was STILL green — the only visible difference being the
// test taking 188s instead of 0.00s while reporting PASS. noteMaxTotalBytes was
// worse: it appeared only inside t.Fatalf MESSAGES, never in an assertion, so
// doubling it and quadrupling it both survived.
//
// Changing a cap is a deliberate act and must change these numbers too. The
// preamble in each test says so by name rather than leaving the next reader to
// infer it from a diff.
const (
	wantNoteTextCap  = 32 * 1024  // noteMaxTextBytes
	wantNoteTotalCap = 256 * 1024 // noteMaxTotalBytes
)

// The cap must BITE (a url past it is not returned) and must not be vacuous (a url
// before it IS returned) — the positive control that separates "the cap worked"
// from "the scanner found nothing at all".
func TestExtractNoteLinksBoundsAHugeNote(t *testing.T) {
	if noteMaxTextBytes != wantNoteTextCap {
		t.Fatalf("noteMaxTextBytes = %d but this guard is calibrated to %d — update the "+
			"literal deliberately, and say why the cap moved", noteMaxTextBytes, wantNoteTextCap)
	}
	early := "https://example.com/early.safetensors"
	late := "https://example.com/late.safetensors"
	// Sized from the LITERAL: exactly one cap's worth of filler, so `late` sits just
	// past the boundary. Sizing it from noteMaxTextBytes is the defect above.
	filler := strings.Repeat("padding words that are not urls ", (wantNoteTextCap/32)+64)
	got := urlsOf(ExtractNoteLinks(FormatUI, uiNoteGraph(t,
		[3]string{"1", "MarkdownNote", early + " " + filler + " " + late})))
	if len(got) != 1 || got[0] != early {
		t.Fatalf("got %v, want exactly [%s] — %q sits past the %d-byte per-note cap",
			got, early, late, wantNoteTextCap)
	}
}

// The WHOLE-GRAPH budget is a second, independent bound: many notes each under the
// per-note cap must still stop.
//
// It is asserted from BOTH sides, which is what makes an off-by-multiple visible:
// a note placed past the budget is not scanned, and a note placed just INSIDE it
// is. With only the first half, doubling the budget survives — the extra headroom
// is simply never used.
func TestExtractNoteLinksBoundsTheWholeGraph(t *testing.T) {
	if noteMaxTotalBytes != wantNoteTotalCap {
		t.Fatalf("noteMaxTotalBytes = %d but this guard is calibrated to %d — update the "+
			"literal deliberately, and say why the budget moved", noteMaxTotalBytes, wantNoteTotalCap)
	}
	// Each filler note is exactly one per-note cap, so N of them consume N caps of
	// the whole-graph budget. Both figures come from the literals.
	const fillerNotes = wantNoteTotalCap / wantNoteTextCap // 8 notes exhaust it exactly
	body := strings.Repeat("x ", wantNoteTextCap/2)
	url := "https://example.com/past-budget.safetensors"

	makeGraph := func(n int) json.RawMessage {
		var nodes [][3]string
		for i := 0; i < n; i++ {
			nodes = append(nodes, [3]string{"1", "MarkdownNote", body})
		}
		return uiNoteGraph(t, append(nodes, [3]string{"99", "MarkdownNote", url})...)
	}

	// PAST the budget: the trailing note is never scanned.
	if got := ExtractNoteLinks(FormatUI, makeGraph(fillerNotes)); got != nil {
		t.Fatalf("got %v after %d full-cap notes, want nil past the %d-byte whole-graph budget",
			urlsOf(got), fillerNotes, wantNoteTotalCap)
	}
	// JUST INSIDE it: one fewer filler note leaves room, and the trailing note IS
	// scanned. This is the half that fails when the budget is raised — without it,
	// any larger budget passes because the extra room is never exercised.
	if got := urlsOf(ExtractNoteLinks(FormatUI, makeGraph(fillerNotes-1))); len(got) != 1 || got[0] != url {
		t.Fatalf("got %v with %d full-cap notes, want [%s] — one cap of budget remains, so "+
			"the trailing note must still be scanned", got, fillerNotes-1, url)
	}
}

// 🔴 boundedText's no-whitespace branch is the SOLE STATED MECHANISM preventing
// truncation from inventing a URL the author never wrote — and nothing reached it.
// The audit replaced `return "", limit` with `return clipped, limit` and the whole
// suite stayed green; a panic() probe in that branch recorded ZERO executions
// across every test in the repo. The code was correct and unverified, which is the
// same thing as unwritten.
//
// The fixture is an over-cap note containing NO whitespace at all, so the clip
// cannot fall back to a word boundary. The url is placed so that clipping mid-way
// through it would leave a SHORTER, STILL-WELL-FORMED url — the exact failure the
// branch exists to prevent, and one a naive "no links found" assertion would miss.
func TestBoundedTextDropsAWhitespacelessOverCapNote(t *testing.T) {
	// One unbroken token, longer than the per-note cap, whose truncation at the cap
	// yields a valid url for a DIFFERENT file.
	const prefix = "https://example.com/aaaa.safetensors/x"
	note := prefix + strings.Repeat("y", wantNoteTextCap) + "/real.safetensors"
	if strings.ContainsAny(note, " \t\r\n") {
		t.Fatal("precondition: the fixture must contain no whitespace, or the clip falls back to a word boundary")
	}
	if len(note) <= noteMaxTextBytes {
		t.Fatalf("precondition: the fixture is %d bytes, which does not exceed the %d-byte cap",
			len(note), noteMaxTextBytes)
	}
	if got := ExtractNoteLinks(FormatUI, uiNoteGraph(t, [3]string{"1", "MarkdownNote", note})); got != nil {
		t.Fatalf("got %v — a whitespaceless over-cap note must yield NOTHING rather than a "+
			"url truncation invented", urlsOf(got))
	}

	// POSITIVE CONTROL: the same note UNDER the cap is scanned and yields its one
	// real url, so the nil above is the truncation guard and not a scanner that
	// cannot read a whitespaceless note at all.
	short := prefix + strings.Repeat("y", 16) + "/real.safetensors"
	got := urlsOf(ExtractNoteLinks(FormatUI, uiNoteGraph(t, [3]string{"1", "MarkdownNote", short})))
	if len(got) != 1 || got[0] != short {
		t.Fatalf("positive control: got %v, want [%s]", got, short)
	}
}

// wantNoteLinkCap is a LITERAL, never noteMaxLinks. Deriving it from the constant
// under test is what let "noteMaxLinks = 640" pass the first mutation round: both
// sides moved together and no mutation could separate them. Changing the cap is a
// deliberate act and must change this number too.
const wantNoteLinkCap = 64

// generatedNoteLinks is a literal well above the cap, for the same reason.
const generatedNoteLinks = 200

func TestExtractNoteLinksCapsTheLinkCount(t *testing.T) {
	if noteMaxLinks != wantNoteLinkCap {
		t.Fatalf("noteMaxLinks = %d but this guard is calibrated to %d — update the literal "+
			"deliberately, and say why the cap moved", noteMaxLinks, wantNoteLinkCap)
	}
	var b strings.Builder
	for i := 0; i < generatedNoteLinks; i++ {
		b.WriteString("https://example.com/f")
		b.WriteString(strings.Repeat("a", i%5))
		b.WriteString(strconv.Itoa(i))
		b.WriteString(".safetensors\n")
	}
	got := ExtractNoteLinks(FormatUI, uiNoteGraph(t, [3]string{"1", "MarkdownNote", b.String()}))
	if len(got) != wantNoteLinkCap {
		t.Fatalf("got %d links from %d distinct urls, want the cap %d",
			len(got), generatedNoteLinks, wantNoteLinkCap)
	}
}

// The https rule is enforced TWICE — the pattern only matches https, and
// parseNoteURL asserts the scheme again — so mutating either layer alone leaves
// behaviour unchanged (measured: loosening the pattern to `https?` survived the
// whole suite). That redundancy is deliberate, and this pins the SECOND layer
// directly, by calling it with input the pattern would never have produced.
func TestParseNoteURLAssertsTheSchemeItself(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/a.safetensors",
		"ftp://example.com/a.safetensors",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"//example.com/a.safetensors",
		"https://",
	} {
		if u, base, ok := parseNoteURL(raw); ok {
			t.Fatalf("parseNoteURL(%q) accepted it as (%q, %q)", raw, u, base)
		}
	}
	// Positive control: the layer does accept a real https url, so the refusals
	// above are not a fact about a parser that refuses everything.
	if _, base, ok := parseNoteURL("https://example.com/a.safetensors"); !ok || base != "a.safetensors" {
		t.Fatalf("positive control: ok=%v base=%q", ok, base)
	}
}

func TestNoteLinkBasename(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors", "zit_sda_v1.safetensors"},
		// A query string is NOT part of url.URL.Path, so it cannot reach the basename.
		{"https://example.com/a.safetensors?download=true", "a.safetensors"},
		{"https://example.com/a.safetensors#section", "a.safetensors"},
		// Percent-escapes decode, so a %20 compares equal to a space in a reference.
		{"https://example.com/my%20model.safetensors", "my model.safetensors"},
		{"https://example.com/dir%2Fa.safetensors", ""}, // decoding must not reintroduce structure
		// Page links address no file at all.
		{"https://civitai.com/models/620406/moody-porn-mix", "moody-porn-mix"},
		{"https://openmodeldb.info/models/1x-SkinContrast-SuperUltraCompact", "1x-SkinContrast-SuperUltraCompact"},
		{"https://example.com/", ""},
		{"https://example.com", ""},
		{"https://example.com/dir/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			links := ExtractNoteLinks(FormatUI, uiNoteGraph(t, [3]string{"1", "Note", tc.url}))
			if len(links) != 1 {
				t.Fatalf("extracted %d links from %q, want 1", len(links), tc.url)
			}
			if links[0].Basename != tc.want {
				t.Fatalf("basename of %q: got %q, want %q", tc.url, links[0].Basename, tc.want)
			}
		})
	}
}

func TestNoteLinksMatchingIsExactBasename(t *testing.T) {
	links := []NoteLink{
		{URL: "https://h/x/zit_sda_v1.safetensors", Basename: "zit_sda_v1.safetensors"},
		{URL: "https://h/x/ZIT_SDA_V1.safetensors", Basename: "ZIT_SDA_V1.safetensors"},
		{URL: "https://h/x/zit_sda_v2.safetensors", Basename: "zit_sda_v2.safetensors"},
		{URL: "https://h/x/my%20model.safetensors", Basename: "my model.safetensors"},
		{URL: "https://civitai.com/models/620406", Basename: ""},
	}
	t.Run("case-insensitive exact match", func(t *testing.T) {
		got := NoteLinksMatching(links, "zit_sda_v1.safetensors")
		if len(got) != 2 {
			t.Fatalf("got %v, want both case spellings", urlsOf(got))
		}
	})
	t.Run("a subfolder-prefixed reference matches on its basename", func(t *testing.T) {
		for _, ref := range []string{"loras/zit_sda_v2.safetensors", `ComfyUI\zit_sda_v2.safetensors`} {
			got := NoteLinksMatching(links, ref)
			if len(got) != 1 || got[0].Basename != "zit_sda_v2.safetensors" {
				t.Fatalf("%q: got %v", ref, urlsOf(got))
			}
		}
	})
	t.Run("a decoded escape matches the plain reference", func(t *testing.T) {
		if got := NoteLinksMatching(links, "my model.safetensors"); len(got) != 1 {
			t.Fatalf("got %v, want the percent-escaped link", urlsOf(got))
		}
	})
	t.Run("a different name never matches", func(t *testing.T) {
		if got := NoteLinksMatching(links, "zit_sda_v9.safetensors"); got != nil {
			t.Fatalf("got %v, want nil", urlsOf(got))
		}
	})
	// 🔴 EXACT, not "contains", in BOTH directions. Without a fixture where one name
	// is a proper substring of the other, loosening == to strings.Contains passes the
	// whole suite — measured: that mutant survived the first round.
	t.Run("a substring relationship is not a match", func(t *testing.T) {
		sub := []NoteLink{
			{URL: "https://h/x/my_model.safetensors", Basename: "my_model.safetensors"},
			{URL: "https://h/x/a.safetensors", Basename: "a.safetensors"},
		}
		// The reference is a proper substring of a link's basename.
		if got := NoteLinksMatching(sub, "model.safetensors"); got != nil {
			t.Fatalf("model.safetensors matched %v — a note link that merely CONTAINS the "+
				"reference is a different file", urlsOf(got))
		}
		// A link's basename is a proper substring of the reference.
		if got := NoteLinksMatching(sub, "prefix_a.safetensors"); got != nil {
			t.Fatalf("prefix_a.safetensors matched %v", urlsOf(got))
		}
		// Positive control: the exact name still matches, so the two nils above are
		// not a fact about a matcher that matches nothing.
		if got := NoteLinksMatching(sub, "a.safetensors"); len(got) != 1 {
			t.Fatalf("positive control: exact match got %v, want 1", urlsOf(got))
		}
	})
	t.Run("an empty basename can never match", func(t *testing.T) {
		for _, ref := range []string{"", "   ", "620406"} {
			if got := NoteLinksMatching(links, ref); got != nil {
				t.Fatalf("%q: got %v, want nil", ref, urlsOf(got))
			}
		}
	})
}

// wf590Note is modelled on the REAL "## Model links" MarkdownNote (node 753) of
// the operator's workflow 590 — the case this whole feature exists for. It keeps
// the shapes that matter: a markdown link whose text is the filename, a bare URL
// wrapped in markdown-link syntax, a github release asset, and a page link with no
// file. Read verbatim out of a copy of the live database on 2026-08-05.
const wf590Note = "## Model links\n\n" +
	"**vae**\n\n" +
	"- [ae.safetensors](https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors)\n\n" +
	"# 📥 Download Links / 下载地址\n\n" +
	"- **Moody Porn Mix** (Hardcore content, 硬核)  \n" +
	"  [https://civitai.com/models/620406/moody-porn-mix](https://civitai.com/models/620406/moody-porn-mix)\n\n" +
	"## ⚡ Z-Image-Turbo-SDA (Diversity Fix Adapter，多样性修复)\n\n" +
	"- **File:** `zit_sda_v1.safetensors`  \n" +
	"  [https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors](https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors)\n\n" +
	"### 🔸 4xNomosWebPhoto_RealPLKSR (Recommended，推荐，更均衡)\n" +
	"- **File:** `4xNomosWebPhoto_RealPLKSR.safetensors`  \n" +
	"  [https://github.com/Phhofm/models/releases/tag/4xNomosWebPhoto_RealPLKSR](https://github.com/Phhofm/models/releases/download/4xNomosWebPhoto_RealPLKSR/4xNomosWebPhoto_RealPLKSR.pth)\n\n" +
	"### 🔸 1x SkinContrast (Skin Enhancement)\n" +
	"- **Model:** `1xSkinContrast-SuperUltraCompact`  \n" +
	"  [https://openmodeldb.info/models/1x-SkinContrast-SuperUltraCompact](https://openmodeldb.info/models/1x-SkinContrast-SuperUltraCompact)\n"

// The end-to-end claim behind the feature: the two files preflight reports missing
// for workflow 590 ARE findable in its note, by exact basename, and nothing else in
// the note is mistaken for them.
func TestExtractNoteLinksFindsWorkflow590sMissingFiles(t *testing.T) {
	graph := uiNoteGraph(t,
		[3]string{"699", "Note", "The final resolution will be：base * 1.7"},
		[3]string{"753", "MarkdownNote", wf590Note},
	)
	links := ExtractNoteLinks(FormatUI, graph)
	if len(links) < 5 {
		t.Fatalf("extracted %d links from the wf590 note, want at least 5: %v", len(links), urlsOf(links))
	}
	for _, l := range links {
		if l.NodeID != "753" {
			t.Fatalf("link %q attributed to node %q, want 753", l.URL, l.NodeID)
		}
	}

	t.Run("zit_sda_v1.safetensors resolves to the HuggingFace file", func(t *testing.T) {
		got := NoteLinksMatching(links, "zit_sda_v1.safetensors")
		if len(got) != 1 {
			t.Fatalf("got %v, want exactly one match", urlsOf(got))
		}
		const want = "https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors"
		if got[0].URL != want {
			t.Fatalf("got %q, want %q", got[0].URL, want)
		}
	})

	t.Run("4xNomosWebPhoto_RealPLKSR.pth resolves to the github release asset", func(t *testing.T) {
		got := NoteLinksMatching(links, "4xNomosWebPhoto_RealPLKSR.pth")
		if len(got) != 1 {
			t.Fatalf("got %v, want exactly one match", urlsOf(got))
		}
		const want = "https://github.com/Phhofm/models/releases/download/4xNomosWebPhoto_RealPLKSR/4xNomosWebPhoto_RealPLKSR.pth"
		if got[0].URL != want {
			t.Fatalf("got %q, want %q", got[0].URL, want)
		}
	})

	t.Run("the .safetensors name written in the prose matches nothing", func(t *testing.T) {
		// The note SAYS `4xNomosWebPhoto_RealPLKSR.safetensors` in its File: line while
		// linking a .pth. Exact-basename matching must not paper over that.
		if got := NoteLinksMatching(links, "4xNomosWebPhoto_RealPLKSR.safetensors"); got != nil {
			t.Fatalf("got %v, want nil", urlsOf(got))
		}
	})

	t.Run("the github release TAG page is extracted but is a different basename", func(t *testing.T) {
		var tagged bool
		for _, l := range links {
			if strings.Contains(l.URL, "/releases/tag/") {
				tagged = true
				if l.Basename != "4xNomosWebPhoto_RealPLKSR" {
					t.Fatalf("tag page basename %q", l.Basename)
				}
			}
		}
		if !tagged {
			t.Fatal("the release tag page was not extracted at all")
		}
	})
}
