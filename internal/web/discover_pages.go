package web

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/library"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// parentDir returns path's parent, or "" when path is already the filesystem
// root or a blocked system directory (so the browser never offers a ".." up into
// a refused location).
func parentDir(path string) string {
	parent := filepath.Dir(path)
	if parent == path || parent == "" {
		return ""
	}
	if library.BlockedForBrowse(parent) {
		return ""
	}
	return parent
}

// jsString renders s as a safe JavaScript string literal for an inline handler.
func jsString(s string) string { return strconv.Quote(s) }

// csrfInline attaches the CSRF token to an htmx-driven button via hx-vals, so a
// control that issues its own POST (outside a submitted form, or alongside one)
// always carries a valid token.
func csrfInline(csrf string) g.Node {
	return hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf))
}

// selectedDirsList renders the persisted extra scan directories as pre-checked
// checkboxes (name "scan_dir"), each with a remove control. An empty selection
// shows a hint. This fragment is swapped in place after add/remove.
func selectedDirsList(dirs []string, csrf string) g.Node {
	if len(dirs) == 0 {
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("No extra directories selected. Discover installs or browse to add one."))
	}
	var rows []g.Node
	for _, d := range dirs {
		rows = append(rows, h.Label(
			h.Class("flex items-center justify-between gap-2 rounded border border-slate-800 bg-slate-900 px-2 py-1"),
			h.Div(
				h.Class("flex items-center gap-2 min-w-0"),
				h.Input(h.Type("checkbox"), h.Name("scan_dir"), h.Value(d), g.Attr("checked"),
					h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500")),
				h.Span(h.Class("truncate text-xs text-slate-300"), g.Text(d)),
			),
			h.Button(
				h.Type("button"),
				hx("post", "/library/scan-dirs/remove"),
				hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, d, csrf)),
				hx("target", "#selected-dirs"),
				hx("swap", "innerHTML"),
				h.Class("shrink-0 text-xs text-rose-400 hover:text-rose-300"),
				g.Text("remove"),
			),
		))
	}
	return h.Div(h.Class("space-y-1"), g.Group(rows))
}

// discoverScanning renders the in-progress fragment: a spinner, the "found N so
// far" copy, a Stop button, the installs streamed so far (each with an Add
// control), and an htmx poller. The element (#discover-poll) polls GET
// /library/discover/status every second and swaps ITSELF (outerHTML) with the
// response — so when the crawl settles and status returns the poller-less results
// fragment, polling stops. The crawl keeps scanning ALL disks in the background
// until the user Stops it or the tree is exhausted.
func discoverScanning(installs []library.Install, selected []string, csrf string) g.Node {
	selSet := map[string]bool{}
	for _, s := range selected {
		selSet[s] = true
	}
	header := h.Div(
		h.Class("flex items-start justify-between gap-2"),
		h.Div(
			h.Class("flex items-center gap-2 text-xs text-slate-400"),
			spinnerGlyph(),
			g.Text(fmt.Sprintf(
				"Scanning all disks for ComfyUI / Automatic1111 installs… found %d so far (large/slow drives can take a while — Stop when you see the one you want)",
				len(installs))),
		),
		discoverStopButton(csrf),
	)
	children := []g.Node{
		h.ID("discover-poll"),
		hx("get", "/library/discover/status"),
		hx("trigger", "every 1s"),
		hx("swap", "outerHTML"),
		h.Class("mt-2 space-y-2"),
		header,
	}
	if len(installs) > 0 {
		var cards []g.Node
		for _, in := range installs {
			// running=true → each Add control carries the "stop the scan?" prompt.
			cards = append(cards, discoverCard(in, selSet[in.Path], true, csrf))
		}
		children = append(children, h.Div(h.Class("space-y-2"), g.Group(cards)))
	}
	return h.Div(children...)
}

// discoverStopButton renders the Stop control shown while a scan runs. It POSTs
// /library/discover/stop (CSRF via hx-vals) and swaps the poller element with the
// server's current status fragment.
func discoverStopButton(csrf string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/library/discover/stop"),
		hx("target", "#discover-poll"),
		hx("swap", "outerHTML"),
		csrfInline(csrf),
		h.Class("shrink-0"),
	}, g.Text("Stop"))
}

// discoverResults renders the TERMINAL auto-discovery result (no poller): each
// install as a card with an Add button, plus a status line. It distinguishes an
// exhausted crawl ("Scan complete — found N") from a user-stopped or cancelled
// one ("Scan stopped — found N"). A completed crawl that found nothing renders
// the plain "no installs" copy.
func discoverResults(installs []library.Install, selected []string, stopped bool, err error, csrf string) g.Node {
	selSet := map[string]bool{}
	for _, s := range selected {
		selSet[s] = true
	}
	// A clean, exhausted crawl that found nothing: the plain no-installs copy.
	if len(installs) == 0 && !stopped && err == nil {
		return h.P(h.Class("text-xs text-slate-500 mt-2"),
			g.Text("No ComfyUI or Automatic1111/Forge installs found in the usual locations."))
	}
	var cards []g.Node
	for _, in := range installs {
		// Terminal fragment: the scan is no longer running, so Add adds silently.
		cards = append(cards, discoverCard(in, selSet[in.Path], false, csrf))
	}
	// "stopped" covers an explicit user Stop AND any other cancellation/error (e.g.
	// server shutdown); only a clean exhaustion reads "complete".
	statusText := fmt.Sprintf("Scan complete — found %d", len(installs))
	statusClass := "text-xs text-emerald-400"
	if stopped || err != nil {
		statusText = fmt.Sprintf("Scan stopped — found %d", len(installs))
		statusClass = "text-xs text-amber-400"
	}
	nodes := []g.Node{
		h.Class("mt-2 space-y-2"),
		g.Group(cards),
		h.P(h.Class(statusClass), g.Text(statusText)),
	}
	return h.Div(nodes...)
}

func discoverCard(in library.Install, added, scanRunning bool, csrf string) g.Node {
	kindLabel := "ComfyUI"
	kindVariant := "indigo"
	if in.Kind == library.KindA1111 {
		kindLabel, kindVariant = "A1111 / Forge", "blue"
	}
	confVariant := "amber"
	if in.Confidence == library.ConfidenceHigh {
		confVariant = "green"
	}

	meta := []g.Node{
		badge(kindLabel, kindVariant),
		badge(strconv.Itoa(len(in.ModelDirs))+" model dirs", "slate"),
		badge(in.Confidence+" confidence", confVariant),
	}
	if in.Git != nil {
		branch := in.Git.Branch
		if branch == "" {
			branch = "git"
		}
		meta = append(meta, badge("⎇ "+branch, "slate"))
		if in.Git.Dirty {
			meta = append(meta, badge("dirty", "red"))
		}
	}

	var action g.Node
	if added {
		action = h.Span(h.Class("text-xs text-emerald-400"), g.Text("added ✓"))
	} else {
		addAttrs := []g.Node{
			h.Type("button"),
			hx("post", "/library/scan-dirs/add"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, in.Path, csrf)),
			hx("target", "#selected-dirs"),
			hx("swap", "innerHTML"),
			h.Class("shrink-0"),
		}
		// Add-mid-scan: when a scan is still running, adding an install likely means
		// the user found what they came for — prompt them to Stop the scan. When no
		// scan runs, Add just adds silently (no prompt).
		if scanRunning {
			addAttrs = append(addAttrs, hx("confirm",
				"Add this install? (the background scan is still running — you can Stop it after)"))
		}
		action = civButton("light", "sm", addAttrs, g.Text("Add"))
	}

	return h.Div(
		h.Class("flex items-center justify-between gap-3 rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(
			h.Class("min-w-0 space-y-1"),
			h.Div(h.Class("truncate text-sm text-slate-200"), g.Text(in.Path)),
			h.Div(h.Class("flex flex-wrap items-center gap-1"), g.Group(meta)),
		),
		action,
	)
}

// directoryBrowser renders the server-side directory-browser control: a path
// input, a "Browse" button, and the results target.
func directoryBrowser(csrf string) g.Node {
	return h.Div(
		h.Class("space-y-2 border-t border-slate-800 pt-2"),
		h.Div(h.Class("text-xs font-medium text-slate-400"), g.Text("Browse server directories")),
		h.Div(
			h.Class("flex items-end gap-2"),
			h.Div(h.Class("flex-1"),
				textInput("text-input", "browse-path", "Path",
					h.Name("browse_path"),
					h.Placeholder("Leave blank for your home directory")),
			),
			civButton("outline", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/library/browse"),
				hx("include", "#browse-path"),
				hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
				hx("target", "#browse-results"),
				hx("swap", "innerHTML"),
			}, g.Text("Browse")),
		),
		h.Div(h.ID("browse-results")),
	)
}

// browseResults renders one directory listing: an "add this directory" control
// (when it is an addable scan root) plus each immediate subdirectory as a
// drill-in button. The path input is updated so navigation is one click.
func browseResults(path string, dirs []browseEntry, canAdd bool, csrf string) g.Node {
	header := []g.Node{
		h.Class("flex items-center justify-between gap-2"),
		h.Span(h.Class("truncate text-xs text-slate-300"), g.Text(path)),
	}
	if canAdd {
		header = append(header, civButton("light", "sm", []g.Node{
			h.Type("button"),
			hx("post", "/library/scan-dirs/add"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, path, csrf)),
			hx("target", "#selected-dirs"),
			hx("swap", "innerHTML"),
			h.Class("shrink-0"),
		}, g.Text("Add this directory")))
	}

	var items []g.Node
	if parent := parentDir(path); parent != "" {
		items = append(items, browseDirButton("..", parent, csrf))
	}
	for _, d := range dirs {
		items = append(items, browseDirButton(d.Name, d.Path, csrf))
	}
	if len(dirs) == 0 {
		items = append(items, h.Li(h.Class("text-xs text-slate-500"), g.Text("(no subdirectories)")))
	}

	return h.Div(
		h.Class("mt-2 space-y-2 rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(header...),
		h.Ul(h.Class("max-h-56 space-y-1 overflow-y-auto"), g.Group(items)),
	)
}

// browseDirButton is one navigable subdirectory row: clicking it re-browses into
// that directory. It also writes the path back into the browse input via a small
// hx-on handler so the current location stays visible.
func browseDirButton(label, path, csrf string) g.Node {
	return h.Li(
		h.Button(
			h.Type("button"),
			hx("post", "/library/browse"),
			hx("vals", fmt.Sprintf(`{"path":%q,"csrf_token":%q}`, path, csrf)),
			hx("target", "#browse-results"),
			hx("swap", "innerHTML"),
			g.Attr("hx-on:click", "document.getElementById('browse-path').value="+jsString(path)),
			h.Class("w-full truncate rounded px-2 py-1 text-left text-xs text-slate-300 hover:bg-slate-800"),
			g.Text("📁 "+label),
		),
	)
}
