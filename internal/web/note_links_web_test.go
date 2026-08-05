package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// The workflow's own note links as a resolution source. See note_links.go for the
// security posture these tests exist to pin.
// ─────────────────────────────────────────────────────────────────────────────

const (
	noteHFURL     = "https://huggingface.co/F16/z-image-turbo-sda/resolve/main/zit_sda_v1.safetensors"
	noteGitHubURL = "https://github.com/Phhofm/models/releases/download/4xNomosWebPhoto_RealPLKSR/4xNomosWebPhoto_RealPLKSR.pth"
	noteFile      = "zit_sda_v1.safetensors"
	noteGHFile    = "4xNomosWebPhoto_RealPLKSR.pth"
)

// noteUIGraph is a UI-format workflow that LOADS two model files and documents
// both in a MarkdownNote — the shape of the operator's workflow 590, reduced.
func noteUIGraph(t *testing.T) string {
	t.Helper()
	note := "## Model links\n\n" +
		"- **File:** `" + noteFile + "`  \n  [" + noteHFURL + "](" + noteHFURL + ")\n\n" +
		"- **File:** `" + noteGHFile + "`  \n  [tag page](" + noteGitHubURL + ")\n\n" +
		"- **Moody Porn Mix** [https://civitai.com/models/620406/moody-porn-mix](https://civitai.com/models/620406/moody-porn-mix)\n"
	b, err := json.Marshal(note)
	if err != nil {
		t.Fatalf("marshal note: %v", err)
	}
	return `{"nodes":[
	  {"id":10,"type":"CheckpointLoaderSimple","mode":0,"widgets_values":["` + noteFile + `"]},
	  {"id":11,"type":"UpscaleModelLoader","mode":0,"widgets_values":["` + noteGHFile + `"]},
	  {"id":753,"type":"MarkdownNote","mode":0,"widgets_values":[` + string(b) + `]}
	],"links":[]}`
}

// newNoteServer builds a server wired for the note-install flow: a loopback
// ComfyUI, a writable comfy_model_path, a fake HuggingFace client and a fake
// civitai downloader. Both fakes are returned so a test can assert which one was
// used — the central claim of this feature.
func newNoteServer(t *testing.T, body []byte) (*Server, *fakeHFClient, *fakeDownloader, string) {
	t.Helper()
	srv, dl, comfyModels := newDownloadServer(t, stubReader{}, "https://dl.example/never", []byte("CIVITAI-BYTES"))
	fake := &fakeHFClient{
		body:     body,
		inRepoOK: true,
		inRepo: &hf.Match{
			Repo: "F16/z-image-turbo-sda", Path: noteFile, FileName: noteFile,
			Revision: "0af71d2c",
			URL:      "https://huggingface.co/F16/z-image-turbo-sda/resolve/0af71d2c/" + noteFile,
			SHA256:   sha256Hex(body), Source: hf.SourceNote,
		},
	}
	srv.hfClientFn = func() hfClient { return fake }
	return srv, fake, dl, comfyModels
}

// postNote issues the install-from-note POST with the given extra form values.
func postNote(t *testing.T, srv *Server, wfID string, extra url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{
		"filename": {noteFile},
		"type":     {"Checkpoint"},
		"note_url": {noteHFURL},
	}
	for k, v := range extra {
		form[k] = v
	}
	return post(t, srv, "/workflows/"+wfID+"/install-from-note", form, true)
}

// ─────────────────────────────────────────────────────────────────────────────
// The seam: stored UI graph → missingResolution → rendered dialog.
// ─────────────────────────────────────────────────────────────────────────────

// preflightFailureResult is the ONE place the failure panel's enrichment happens.
// This drives it for real — with a real stored workflow — so the test cannot pass
// while the note-link pass is wired to nothing, and so it fails if the pass is ever
// handed something other than the workflow whose notes it is meant to read.
func TestPreflightFailureResultReadsTheWorkflowsOwnNotes(t *testing.T) {
	srv, _, _, _ := newNoteServer(t, []byte("W"))
	graph := noteUIGraph(t)
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, graph)
	wfNum, perr := strconv.ParseInt(wfID, 10, 64)
	if perr != nil {
		t.Fatalf("workflow id %q: %v", wfID, perr)
	}
	wf, err := srv.store.GetWorkflow(context.Background(), wfNum)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}

	apiGraph := json.RawMessage(`{"10":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"` + noteFile + `"}}}`)
	report := &comfy.PreflightReport{MissingModels: []string{noteFile}}
	res := srv.preflightFailureResult(context.Background(), wf, apiGraph, comfy.ObjectInfo{}, report)

	// Precondition: the fixture actually reached the interesting case.
	if len(res.MissingModels) != 1 || res.MissingModels[0].Filename != noteFile {
		t.Fatalf("MissingModels = %+v, want the one missing file", res.MissingModels)
	}
	got := res.MissingResolved[noteFile]
	if len(got.NoteLinks) != 1 {
		t.Fatalf("NoteLinks = %+v, want exactly the HuggingFace link for %s", got.NoteLinks, noteFile)
	}
	if got.NoteLinks[0].URL != noteHFURL {
		t.Fatalf("NoteLinks[0].URL = %q, want %q", got.NoteLinks[0].URL, noteHFURL)
	}
	if !got.NoteLinks[0].AutoFetchable {
		t.Fatal("a huggingface.co /resolve/ URL with the HF client present must be auto-fetchable")
	}
	if got.NoteLinks[0].Host != "huggingface.co" {
		t.Fatalf("Host = %q", got.NoteLinks[0].Host)
	}
}

// 🔴 The api graph has no notes left — conversion drops Note/MarkdownNote — so a
// pass that read the CONVERTED graph would find nothing. This pins that the UI
// graph is the source, from the same entry point as the test above.
func TestPreflightFailureResultFindsNoNotesInAnAPIFormatWorkflow(t *testing.T) {
	srv, _, _, _ := newNoteServer(t, []byte("W"))
	// The SAME note text, stored as an api-format workflow. Nothing may be offered.
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI, noteUIGraph(t))
	wfNum, perr := strconv.ParseInt(wfID, 10, 64)
	if perr != nil {
		t.Fatalf("workflow id %q: %v", wfID, perr)
	}
	wf, err := srv.store.GetWorkflow(context.Background(), wfNum)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	apiGraph := json.RawMessage(`{"10":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"` + noteFile + `"}}}`)
	res := srv.preflightFailureResult(context.Background(), wf, apiGraph, comfy.ObjectInfo{},
		&comfy.PreflightReport{MissingModels: []string{noteFile}})
	if links := res.MissingResolved[noteFile].NoteLinks; links != nil {
		t.Fatalf("NoteLinks = %+v for an api-format workflow, want nil", links)
	}
}

// The rendered dialog: a HuggingFace link gets a real POSTing control, a
// github.com link gets a link and NO control at all. Both assert STATE — an id
// plus an attribute, and the presence/absence of the endpoint in an hx-post — never
// a sentence another feature could spell.
func TestFixDialogRendersNoteLinksByFetchability(t *testing.T) {
	mm := comfy.MissingModel{Filename: noteFile, CivitaiType: "Checkpoint"}
	const dlgID = "fix-model-0"

	t.Run("a huggingface link renders an install control", func(t *testing.T) {
		offers := []noteLinkOffer{{URL: noteHFURL, Basename: noteFile, Host: "huggingface.co", AutoFetchable: true}}
		out := renderString(t, noteLinkSection(dlgID, mm, offers, 7, "tok", true))
		// SAME-ELEMENT assertion: the id and the state attribute must sit in ONE
		// opening tag. Two bare substring checks would be satisfied by the attribute
		// living on some other element entirely — a shape this repo has shipped.
		tag := openingTagWithID(t, out, "fix-model-0-note-links")
		if !strings.Contains(tag, `data-note-links="1"`) {
			t.Fatalf("the section element carries no note-link count:\n%s", tag)
		}
		if !strings.Contains(out, `hx-post="/workflows/7/install-from-note"`) {
			t.Fatalf("no install control posting to the note endpoint:\n%s", out)
		}
		if !strings.Contains(out, `aria-label="Install `+noteFile+` from huggingface.co and run"`) {
			t.Fatalf("the control has no distinguishing accessible name:\n%s", out)
		}
	})

	t.Run("a non-huggingface link is a link and posts nothing", func(t *testing.T) {
		offers := []noteLinkOffer{{URL: noteGitHubURL, Basename: noteGHFile, Host: "github.com", AutoFetchable: false}}
		out := renderString(t, noteLinkSection(dlgID, mm, offers, 7, "tok", true))
		if strings.Contains(out, "hx-post") {
			t.Fatalf("a link-only host must render NO posting control:\n%s", out)
		}
		if !strings.Contains(out, `href="`+noteGitHubURL+`"`) ||
			!strings.Contains(out, `rel="noopener noreferrer"`) ||
			!strings.Contains(out, `target="_blank"`) {
			t.Fatalf("the external link is missing or unsafe:\n%s", out)
		}
	})

	t.Run("an ineligible install degrades the huggingface link to a link", func(t *testing.T) {
		offers := []noteLinkOffer{{URL: noteHFURL, Basename: noteFile, Host: "huggingface.co", AutoFetchable: true}}
		out := renderString(t, noteLinkSection(dlgID, mm, offers, 7, "tok", false))
		if strings.Contains(out, "hx-post") {
			t.Fatalf("comfyDownloadEligible=false must render no control:\n%s", out)
		}
		if !strings.Contains(out, `href="`+noteHFURL+`"`) {
			t.Fatalf("the link must still be reachable:\n%s", out)
		}
	})

	t.Run("no offers renders nothing at all", func(t *testing.T) {
		if n := noteLinkSection(dlgID, mm, nil, 7, "tok", true); n != nil {
			t.Fatalf("want a nil node for a workflow whose notes name no matching file, got %s", renderString(t, n))
		}
	})
}

// Note text is untrusted: a URL is escaped where it is rendered, and a hostile
// note cannot break out of an attribute or inject markup.
func TestNoteLinkRenderingEscapesUntrustedText(t *testing.T) {
	mm := comfy.MissingModel{Filename: noteFile, CivitaiType: "Checkpoint"}
	hostile := `https://evil.example/"><script>alert(1)</script>/a.safetensors`
	offers := []noteLinkOffer{{URL: hostile, Basename: `a"><b>.safetensors`, Host: `evil"example`, AutoFetchable: false}}
	out := renderString(t, noteLinkSection("fix-model-0", mm, offers, 7, "tok", true))
	if strings.Contains(out, "<script>") {
		t.Fatalf("unescaped markup reached the page:\n%s", out)
	}
	if strings.Contains(out, `<b>`) {
		t.Fatalf("unescaped markup from the basename reached the page:\n%s", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The endpoint.
// ─────────────────────────────────────────────────────────────────────────────

// The end-to-end claim: a note-linked HuggingFace file installs, through the
// HARDENED HuggingFace client, pinned to the repo's commit sha and verified
// against its LFS oid — and the civitai downloader is never touched.
//
// The civitai downloader's zero call count is backed by a POSITIVE CONTROL below
// (TestNoteInstallCivitaiDownloaderPositiveControl): a zero from a counter nothing
// can ever increment says nothing.
func TestInstallFromNoteFetchesThroughTheHardenedHFClient(t *testing.T) {
	body := []byte("ZIT-SDA-WEIGHTS")
	srv, fake, dl, comfyModels := newNoteServer(t, body)
	rr := &runRecorder{}
	srv.runFn = rr.fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	resp := postNote(t, srv, wfID, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("install-from-note = %d", resp.Code)
	}
	pollRunUntilDone(t, srv, wfID)

	dest := filepath.Join(comfyModels, "checkpoints", noteFile)
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("model not written to %s: %v", dest, err)
	}
	if string(got) != string(body) {
		t.Fatalf("dest content = %q, want %q", got, body)
	}
	if fake.dlCalls != 1 {
		t.Fatalf("hardened HuggingFace client called %d times, want 1", fake.dlCalls)
	}
	if dl.calls != 0 {
		t.Fatalf("the civitai downloader was called %d times for a note URL, want 0", dl.calls)
	}
	// The repo + basename came out of the URL, and the metadata lookup happened.
	if len(fake.inRepoArgs) != 1 || fake.inRepoArgs[0] != [2]string{"F16/z-image-turbo-sda", noteFile} {
		t.Fatalf("ResolveInRepo args = %v, want one (repo, basename) pair from the URL", fake.inRepoArgs)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.calls != 1 {
		t.Fatalf("runFn called %d times, want 1", rr.calls)
	}
}

// POSITIVE CONTROL for the assertion above. The same server, the same fake civitai
// downloader — driven down the ORDINARY download-and-run path — must register
// exactly one call. Without this, "the civitai downloader was called 0 times" is
// indistinguishable from a counter wired to nothing.
func TestNoteInstallCivitaiDownloaderPositiveControl(t *testing.T) {
	const dlURL = "https://dl.example/checkpoint"
	reader := dlRunReader{searchRaw: searchRawWithFile(t, noteFile, dlURL)}
	srv, dl, _ := newDownloadServer(t, reader, dlURL, []byte("CIVITAI-BYTES"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	rec := post(t, srv, "/workflows/"+wfID+"/download-and-run", url.Values{
		"filename": {noteFile}, "type": {"Checkpoint"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download-and-run = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, wfID)
	if dl.calls != 1 {
		t.Fatalf("civitai downloader called %d times on its own path, want exactly 1 — "+
			"the zero asserted by the note test would otherwise prove nothing", dl.calls)
	}
}

// 🔴 THE BINDING CHECK. The endpoint may only fetch a URL this workflow's own notes
// contain. Everything else is a 400 with NO egress of any kind.
func TestInstallFromNoteRefusesAURLTheWorkflowDoesNotLink(t *testing.T) {
	srv, fake, dl, _ := newNoteServer(t, []byte("W"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	cases := map[string]string{
		"a url nobody wrote in this graph": "https://huggingface.co/evil/repo/resolve/main/" + noteFile,
		"the same url with a query added":  noteHFURL + "?x=1",
		"the same url with a trailing dot": noteHFURL + ".",
		"an http downgrade":                strings.Replace(noteHFURL, "https://", "http://", 1),
		"an empty url":                     "",
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			resp := postNote(t, srv, wfID, url.Values{"note_url": {u}})
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.Code)
			}
		})
	}
	if fake.dlCalls != 0 || len(fake.inRepoArgs) != 0 || dl.calls != 0 {
		t.Fatalf("a refused url still caused egress: hf dl=%d, hf lookups=%d, civitai=%d",
			fake.dlCalls, len(fake.inRepoArgs), dl.calls)
	}
}

// A url the workflow DOES link but whose host no hardened client covers must
// decline with a reason and fetch nothing — with either downloader.
func TestInstallFromNoteIsLinkOnlyForANonHuggingFaceHost(t *testing.T) {
	srv, fake, dl, comfyModels := newNoteServer(t, []byte("W"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	resp := postNote(t, srv, wfID, url.Values{
		"filename": {noteGHFile},
		"type":     {"Upscaler"},
		"note_url": {noteGitHubURL},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a reason", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, html.EscapeString(noteReasonNotFetchable)) {
		t.Fatalf("no not-fetchable reason in the response:\n%s", body)
	}
	if fake.dlCalls != 0 || len(fake.inRepoArgs) != 0 {
		t.Fatalf("the HuggingFace client was used for a github.com url (dl=%d lookups=%d)",
			fake.dlCalls, len(fake.inRepoArgs))
	}
	if dl.calls != 0 {
		t.Fatalf("the civitai downloader fetched a github.com url %d times — it has no host "+
			"allowlist and must never see one", dl.calls)
	}
	// Nothing landed on disk anywhere under the models root.
	assertNoFilesUnder(t, comfyModels)
}

// 🔴 With the HuggingFace fallback disabled there is no hardened client, and
// modelDownloader would silently fall back to the civitai downloader. The handler
// must refuse BEFORE building a plan rather than let that happen.
func TestInstallFromNoteRefusesWhenTheHardenedClientIsAbsent(t *testing.T) {
	srv, _, dl, comfyModels := newNoteServer(t, []byte("W"))
	srv.hfClientFn = nil // and HFFallback is false in this Config
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	resp := postNote(t, srv, wfID, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	if body := resp.Body.String(); !strings.Contains(body, html.EscapeString(noteReasonHFDisabled)) {
		t.Fatalf("no hf-disabled reason:\n%s", body)
	}
	if dl.calls != 0 {
		t.Fatalf("the civitai downloader was handed a HuggingFace url %d times", dl.calls)
	}
	assertNoFilesUnder(t, comfyModels)
}

// The bytes must be pinnable. A gated repo and a file with no LFS oid are both
// refused rather than downloaded unverified — this is the LAST place to relax the
// verification every other HuggingFace install in this app performs.
func TestInstallFromNoteRefusesAnUnpinnableFile(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*hf.Match)
		reason string
	}{
		{"a gated repo", func(m *hf.Match) { m.Gated = true }, noteReasonGated},
		{"no content hash", func(m *hf.Match) { m.SHA256 = "" }, noteReasonNoHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fake, dl, comfyModels := newNoteServer(t, []byte("W"))
			tc.mutate(fake.inRepo)
			srv.runFn = (&runRecorder{}).fn()
			wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

			resp := postNote(t, srv, wfID, nil)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d", resp.Code)
			}
			if body := resp.Body.String(); !strings.Contains(body, html.EscapeString(tc.reason)) {
				t.Fatalf("want the %q reason, got:\n%s", tc.name, body)
			}
			if fake.dlCalls != 0 || dl.calls != 0 {
				t.Fatalf("bytes were fetched anyway (hf=%d civitai=%d)", fake.dlCalls, dl.calls)
			}
			assertNoFilesUnder(t, comfyModels)
		})
	}
}

// A repo that no longer has the file declines with a reason instead of installing
// whatever else is in there.
func TestInstallFromNoteRefusesARepoMiss(t *testing.T) {
	srv, fake, _, comfyModels := newNoteServer(t, []byte("W"))
	fake.inRepoOK, fake.inRepo = false, nil
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	resp := postNote(t, srv, wfID, nil)
	if body := resp.Body.String(); !strings.Contains(body, html.EscapeString(noteReasonRepoMiss)) {
		t.Fatalf("no repo-miss reason:\n%s", body)
	}
	if fake.dlCalls != 0 {
		t.Fatalf("downloaded despite a miss (%d calls)", fake.dlCalls)
	}
	assertNoFilesUnder(t, comfyModels)
}

// 🔴 OFFER, DON'T PERFORM. A note URL whose basename differs from the workflow's
// reference must be OFFERED — and the approval must name the exact remote file.
func TestInstallFromNoteOffersASubstitutionInsteadOfPerformingIt(t *testing.T) {
	body := []byte("OTHER-WEIGHTS")
	srv, fake, dl, comfyModels := newNoteServer(t, body)
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	// The workflow references noteGHFile; the posted note url delivers noteFile.
	// Both are genuinely in this graph, so the binding check passes and the
	// SUBSTITUTION rule is what has to stop it.
	base := url.Values{"filename": {noteGHFile}, "type": {"Checkpoint"}, "note_url": {noteHFURL}}

	t.Run("the first click offers and downloads nothing", func(t *testing.T) {
		resp := postNote(t, srv, wfID, base)
		out := resp.Body.String()
		if !strings.Contains(out, `data-note-substitute-offer="`+noteFile+`"`) {
			t.Fatalf("no substitution offer naming the remote file:\n%s", out)
		}
		if !strings.Contains(out, html.EscapeString(substituteOfferText(noteGHFile, noteFile))) {
			t.Fatalf("the offer does not name BOTH files:\n%s", out)
		}
		if fake.dlCalls != 0 || dl.calls != 0 || len(fake.inRepoArgs) != 0 {
			t.Fatalf("the offer click caused egress (hf dl=%d lookups=%d civitai=%d)",
				fake.dlCalls, len(fake.inRepoArgs), dl.calls)
		}
		assertNoFilesUnder(t, comfyModels)
	})

	t.Run("confirm_substitute alone is not enough", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("confirm_substitute", "1")
		out := postNote(t, srv, wfID, v).Body.String()
		if !strings.Contains(out, `data-note-substitute-offer=`) {
			t.Fatalf("an unbound approval installed something:\n%s", out)
		}
		if fake.dlCalls != 0 {
			t.Fatalf("downloaded on an unbound approval (%d calls)", fake.dlCalls)
		}
	})

	t.Run("an approval naming a DIFFERENT file is not enough", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("confirm_substitute", "1")
		v.Set("confirm_file", "something_else.safetensors")
		out := postNote(t, srv, wfID, v).Body.String()
		if !strings.Contains(out, `data-note-substitute-offer=`) {
			t.Fatalf("an approval for another file installed this one:\n%s", out)
		}
		if fake.dlCalls != 0 {
			t.Fatalf("downloaded on a mismatched approval (%d calls)", fake.dlCalls)
		}
	})

	t.Run("both together install it under the reference name", func(t *testing.T) {
		v := cloneValues(base)
		v.Set("confirm_substitute", "1")
		v.Set("confirm_file", noteFile)
		resp := postNote(t, srv, wfID, v)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d", resp.Code)
		}
		pollRunUntilDone(t, srv, wfID)
		// The bytes are the REMOTE file's; the name on disk is the workflow's.
		dest := filepath.Join(comfyModels, "checkpoints", noteGHFile)
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("confirmed substitution not written to %s: %v", dest, err)
		}
		if string(got) != string(body) {
			t.Fatalf("dest content = %q, want the remote file's bytes %q", got, body)
		}
		if fake.dlCalls != 1 {
			t.Fatalf("hf downloads = %d, want 1", fake.dlCalls)
		}
	})
}

// A type with no ComfyUI folder has no defined destination, so the install
// declines rather than guessing one.
func TestInstallFromNoteRefusesAnUnroutableType(t *testing.T) {
	srv, fake, _, comfyModels := newNoteServer(t, []byte("W"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	resp := postNote(t, srv, wfID, url.Values{"type": {"NotAType"}})
	if body := resp.Body.String(); !strings.Contains(body, html.EscapeString(noteReasonNoDestination)) {
		t.Fatalf("no destination reason:\n%s", body)
	}
	if fake.dlCalls != 0 {
		t.Fatalf("downloaded without a destination (%d calls)", fake.dlCalls)
	}
	assertNoFilesUnder(t, comfyModels)
}

// The endpoint carries the same prologue as every other run-starting POST: CSRF
// first, then the loopback gate, then the workflow binding.
func TestInstallFromNoteRequiresCSRFAndAReferencedFile(t *testing.T) {
	srv, fake, _, _ := newNoteServer(t, []byte("W"))
	srv.runFn = (&runRecorder{}).fn()
	wfID := seedWorkflow(t, srv, store.WorkflowFormatUI, noteUIGraph(t))

	t.Run("no csrf token", func(t *testing.T) {
		rec := post(t, srv, "/workflows/"+wfID+"/install-from-note", url.Values{
			"filename": {noteFile}, "type": {"Checkpoint"}, "note_url": {noteHFURL},
		}, false)
		if rec.Code == http.StatusOK {
			t.Fatal("a POST with no CSRF token succeeded")
		}
	})
	t.Run("a filename this workflow does not reference", func(t *testing.T) {
		resp := postNote(t, srv, wfID, url.Values{"filename": {"unrelated.safetensors"}})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.Code)
		}
	})
	t.Run("a missing note_url", func(t *testing.T) {
		resp := postNote(t, srv, wfID, url.Values{"note_url": {""}})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.Code)
		}
	})
	t.Run("an unknown workflow", func(t *testing.T) {
		resp := postNote(t, srv, "999999", nil)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.Code)
		}
	})
	if fake.dlCalls != 0 {
		t.Fatalf("a refused request still downloaded (%d calls)", fake.dlCalls)
	}
}

// assertNoFilesUnder fails if any regular file exists under root — the strongest
// available statement of "nothing was installed".
func assertNoFilesUnder(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			t.Fatalf("unexpected file written: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// openingTagWithID returns the single opening tag that carries id="want", so a
// test can assert that another attribute is on THAT element rather than merely
// somewhere in the document.
func openingTagWithID(t *testing.T, out, want string) string {
	t.Helper()
	i := strings.Index(out, `id="`+want+`"`)
	if i < 0 {
		t.Fatalf("no element with id=%q in:\n%s", want, out)
	}
	start := strings.LastIndex(out[:i], "<")
	end := strings.Index(out[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not bound the opening tag around id=%q", want)
	}
	return out[start : i+end+1]
}
