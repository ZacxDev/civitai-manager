package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// hfClient is the HuggingFace fallback surface the resolve/download flow depends on:
// a resolver (filename → Match) and a hardened downloader (mirrors the civitai
// Downloader shape). *hf.Client satisfies it; tests supply a fake.
type hfClient interface {
	Resolve(ctx context.Context, refName string) (*hf.Match, bool, error)
	DownloadFile(ctx context.Context, fileURL string) (*http.Response, error)
}

// compile-time assertion that the live hf client satisfies the seam.
var _ hfClient = (*hf.Client)(nil)

// hfClientOrNil returns the HuggingFace fallback client, or nil when the fallback is
// disabled (config hf_fallback: false). The live client is built per call (cheap),
// carrying the optional HF token (sent only to HuggingFace hosts by the hardened
// client). Tests inject hfClientFn.
func (s *Server) hfClientOrNil() hfClient {
	if s.hfClientFn != nil {
		return s.hfClientFn()
	}
	if !s.cfg.HFFallback {
		return nil
	}
	return hf.NewClient(s.cfg.HFToken)
}

// hfResolveBudget bounds a single HuggingFace resolution (a few small API calls).
const hfResolveBudget = 15 * time.Second

// resolveHF tries the HuggingFace fallback for a missing model reference. It returns
// nil when the fallback is disabled, the resolver errors, or nothing matched — the
// caller then keeps the CivitAI-only degrade. It is an OUTBOUND network call: never
// call it from a render/poll path (only at run-settle or on the explicit install POST).
func (s *Server) resolveHF(parent context.Context, refName string) *hf.Match {
	client := s.hfClientOrNil()
	if client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, hfResolveBudget)
	defer cancel()
	m, ok, err := client.Resolve(ctx, refName)
	if err != nil {
		s.log.Warn("huggingface fallback resolve failed", "ref", refName, "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	return m
}

// hfInstallEligible reports whether an HF match may be auto-installed here: the match
// itself is auto-download-eligible (curated/recognized-org + non-gated + exact + sha),
// the comfy download flow is available (comfy_model_path + local ComfyUI), and the
// match's ComfyUI subdir is set (a routable destination).
func (s *Server) hfInstallEligible(m *hf.Match) bool {
	if m == nil || !m.AutoDownloadEligible() {
		return false
	}
	if !s.comfyDownloadEligible() {
		return false
	}
	return strings.TrimSpace(m.Subdir) != ""
}

// hfMatchSection renders the HuggingFace fallback match inside the Fix popover, used
// when CivitAI yielded no match. It shows the repo + file and either an "Install and
// run" CTA (only when installEligible — auto-install is allowed here) or an "Open on
// HuggingFace ↗" scheme-validated external link with the reason it is link-only.
// installEligible is computed at run-settle (s.hfInstallEligible); rendering is
// decoupled from the server so the popover fragment has no outbound side effects.
// Every untrusted string (repo, file, license) is escaped via g.Text.
func hfMatchSection(mm comfy.MissingModel, m *hf.Match, installEligible bool, wfID int64, csrf string) g.Node {
	body := []g.Node{
		h.H3(h.Class("text-sm font-semibold text-slate-200"),
			g.Text("Found on HuggingFace")),
		h.P(h.Class("text-xs text-slate-400 mb-2"),
			g.Text("Not on CivitAI — matched on HuggingFace. Verify this is the file you want.")),
		hfRepoLine(m),
	}
	if installEligible {
		body = append(body, hfInstallButton(mm, wfID, csrf))
	} else {
		body = append(body, hfLinkOnly(m))
	}
	return h.Div(g.Group(body))
}

// hfRepoLine renders the repo/path + popularity + license line as escaped text with a
// deep link to the HuggingFace repo page (scheme-validated external link).
func hfRepoLine(m *hf.Match) g.Node {
	meta := []g.Node{
		h.Div(h.Class("font-mono text-xs text-slate-300 break-all"),
			g.Text(m.Repo+"/"+m.Path)),
	}
	sub := []string{}
	if m.Downloads > 0 {
		sub = append(sub, humanCount(m.Downloads)+" downloads")
	}
	if m.License != "" {
		sub = append(sub, "license: "+m.License)
	}
	if m.Gated {
		sub = append(sub, "gated")
	}
	if len(sub) > 0 {
		meta = append(meta, h.Div(h.Class("text-xs text-slate-500 mt-0.5"),
			g.Text(strings.Join(sub, " · "))))
	}
	return h.Div(h.Class("rounded border border-slate-800 p-2"), g.Group(meta))
}

// hfInstallButton is the "Install and run" CTA for an eligible HF match. It POSTs the
// (CSRF-carrying) download-and-run request WITHOUT a model_id (so the handler re-runs
// the CivitAI-miss → HF fallback and installs the sha-verified HF file), swapping the
// stable #run-status container.
func hfInstallButton(mm comfy.MissingModel, wfID int64, csrf string) g.Node {
	return h.Div(h.Class("mt-2"),
		civButton("filled", "sm", []g.Node{
			h.Type("button"),
			hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/download-and-run"),
			hx("target", "#"+runStatusContainerID),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "this"),
			hfInstallVals(csrf, mm),
		}, g.Text("Install and run")),
	)
}

// hfLinkOnly renders the degrade path: a scheme-validated "Open on HuggingFace ↗"
// external link plus a one-line reason (gated, or not in the trusted set).
func hfLinkOnly(m *hf.Match) g.Node {
	reason := "This repo isn't in the trusted auto-install set — open it on HuggingFace to review and download it yourself."
	if m.Gated {
		reason = "This HuggingFace repo is gated — accept the model's terms on HuggingFace, then download it there."
	}
	return h.Div(h.Class("mt-2 space-y-1"),
		hfOpenLink(m),
		h.P(h.Class("text-xs text-slate-500"), g.Text(reason)),
	)
}

// hfOpenLink is the scheme-validated external link to the HuggingFace repo page
// (mirrors the Apps click-to-play external-link pattern).
func hfOpenLink(m *hf.Match) g.Node {
	href := m.PageURL()
	if !strings.HasPrefix(href, "https://huggingface.co/") {
		href = "https://huggingface.co/" // defensive: never emit a non-HF href
	}
	return h.A(
		h.Href(href),
		h.Target("_blank"),
		h.Rel("noopener noreferrer"),
		h.Class("text-sm text-indigo-400 hover:underline"),
		g.Text("Open on HuggingFace ↗"),
	)
}

// hfInstallVals builds the hx-vals JSON for an HF install POST: the CSRF token, the
// missing filename, and the inferred type. No model_id — the handler resolves via HF.
// json.Marshal escapes any quote/backslash in the untrusted filename; gomponents then
// HTML-escapes the attribute value (matching the repo's csrfInline posture).
func hfInstallVals(csrf string, mm comfy.MissingModel) g.Node {
	b, _ := json.Marshal(map[string]string{
		"csrf_token": csrf,
		"filename":   mm.Filename,
		"type":       mm.CivitaiType,
	})
	return hx("vals", string(b))
}
