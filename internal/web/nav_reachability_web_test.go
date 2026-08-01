package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestOutputsStaysReachableFromTheRail is the guard the nav rework owes: the
// "Outputs" nav entry was DELETED, so /outputs' only in-app entry point is now
// the recent-outputs rail. Deleting either rail link would orphan a routed page
// with nothing failing.
//
// The fixture seeds a generation FIRST and asserts the rail is actually present
// before checking its links — without that, a rail-free page would satisfy every
// "the nav does not contain /outputs" claim while the positive half silently
// checked nothing.
func TestOutputsStaysReachableFromTheRail(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "alpha")
	seedGen(t, srv, root, &wf, "alpha", []byte("X"))

	// Any FULL page will do — the rail and the nav are app shell, rendered on all
	// of them. It is deliberately not "/", which is a redirect to /search now
	// (handleHome) and therefore has no body to slice.
	rec := get(t, srv, librarySubscriptionsHref)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", librarySubscriptionsHref, rec.Code)
	}
	body := rec.Body.String()

	// The interesting case must be reached: the rail renders only when at least
	// one generation exists.
	if !strings.Contains(body, `id="cm-rail"`) {
		t.Fatalf("fixture did not produce a rail — the link assertions below would be vacuous:\n%s", firstN(body, 800))
	}

	// The nav no longer offers it...
	nav := sliceBetween(t, body, "<nav ", "</nav>")
	if strings.Contains(nav, `href="/outputs"`) {
		t.Errorf("the nav must no longer carry an Outputs entry:\n%s", nav)
	}

	// ...so BOTH rail affordances must survive: the heading link (the primary one
	// after the rework) and the foot "view all" link.
	rail := sliceBetween(t, body, `<aside id="cm-rail"`, "</aside>")
	if !strings.Contains(rail, `href="/outputs" class="cm-rail-title cm-rail-title-link"`) {
		t.Errorf("the rail HEADING must link to /outputs — it is the primary entry point now:\n%s", rail)
	}
	if !strings.Contains(rail, `href="/outputs" class="cm-rail-all"`) {
		t.Errorf("the rail foot must keep its 'view all' link to /outputs:\n%s", rail)
	}
}

// TestTrashRedirectsToDisks pins the compatibility half of replacing the Trash
// nav entry: the OLD url must still land somewhere sensible for anyone who
// bookmarked it, and the restore POST must be untouched.
func TestTrashRedirectsToDisks(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")

	rec := get(t, srv, "/trash")
	if rec.Code != http.StatusFound {
		t.Errorf("GET /trash = %d, want 302 Found (a 301 would be cached indefinitely — see handleTrashRedirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/disks" {
		t.Errorf("GET /trash Location = %q, want /disks", loc)
	}
	// The destination must actually exist — a redirect to a 404 is worse than no
	// redirect, and nothing else in this test would notice.
	if code := get(t, srv, "/disks").Code; code != http.StatusOK {
		t.Errorf("the redirect target /disks returned %d", code)
	}
}

// TestNavDestinationsAllResolve walks every destination the nav actually renders
// and proves each one is routed. It parses the hrefs OUT of the rendered nav
// rather than listing them, so adding a nav entry that points at nothing — or
// renaming a route from under an existing entry — fails here without anyone
// remembering to update a list.
func TestNavDestinationsAllResolve(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")
	body := get(t, srv, librarySubscriptionsHref).Body.String()
	nav := sliceBetween(t, body, "<nav ", "</nav>")

	hrefs := hrefsIn(nav)
	// The fixture must have found the whole nav. The brand link, the four
	// top-level destinations (/search, /workflows/discover, /apps/discover,
	// /disks) and the Library menu's three items == 8. A parser that silently
	// found two would make the loop below prove almost nothing.
	if len(hrefs) < 8 {
		t.Fatalf("only %d nav hrefs parsed (%v) — the extractor is broken, not the nav", len(hrefs), hrefs)
	}
	for _, href := range hrefs {
		rec := get(t, srv, href)
		// 200 or a redirect are both "routed". Anything else — 404 in particular —
		// means the nav is advertising a destination that does not exist.
		if rec.Code != http.StatusOK && rec.Code/100 != 3 {
			t.Errorf("nav destination %s returned %d — the nav links to a route that is not registered", href, rec.Code)
		}
	}
}

// TestSubscriptionsStaysReachableFromTheNav is the guard the "/" rework owes,
// and it is the exact orphaning this change had to avoid.
//
// The subscriptions page (add-a-subscription + the library-derived suggestions +
// the subscription list + the queue + the activity feed) used to BE "/", reached
// through the brand wordmark and nothing else — the nav rework had already
// deleted its "Dashboard" entry. Now that "/" redirects to /search, the wordmark
// no longer leads there, so the Library menu entry is its ONLY in-app entry
// point. Deleting that one anchor would leave a routed, fully-functional page
// that no click in the app can reach, and nothing else in the suite would fail.
//
// 🔴 THREE THINGS, and each one alone is satisfiable while the page is orphaned:
// the link must be IN THE NAV (not merely somewhere on some page), it must
// RESOLVE, and what it resolves to must be THAT PAGE (an href pointing at a
// different 200 would pass the first two).
func TestSubscriptionsStaysReachableFromTheNav(t *testing.T) {
	srv, _ := newOutputsServer(t, "127.0.0.1:8787")
	body := get(t, srv, librarySubscriptionsHref).Body.String()
	nav := sliceBetween(t, body, "<nav ", "</nav>")

	// FIXTURE REACH: the brand no longer leads here, which is WHY the menu entry is
	// load-bearing. If the brand ever pointed at the subscriptions page again this
	// guard would be measuring the wrong thing, so it says so instead of passing.
	brand := sliceBetween(t, nav, "<a ", "</a>")
	if strings.Contains(brand, `href="`+librarySubscriptionsHref+`"`) {
		t.Fatalf("the brand link points at %s again — this guard assumes it does not:\n%s",
			librarySubscriptionsHref, brand)
	}

	// 1. It is in the nav, specifically in the Library menu panel.
	menu := navMenuFragment(t, nav)
	want := `href="` + librarySubscriptionsHref + `" class="cm-navmenu-item">` + subscriptionsPageTitle
	if !strings.Contains(menu, want) {
		t.Fatalf("the Library menu must carry %q -> %s; without it the page has NO in-app entry point:\n%s",
			subscriptionsPageTitle, librarySubscriptionsHref, menu)
	}

	// 2 + 3. It resolves, and to the subscriptions page itself.
	rec := get(t, srv, librarySubscriptionsHref)
	if rec.Code != http.StatusOK {
		t.Fatalf("the nav entry %s returned %d", librarySubscriptionsHref, rec.Code)
	}
	dest := rec.Body.String()
	if !strings.Contains(dest, ">"+subscriptionsPageTitle+"</h1>") {
		t.Errorf("%s does not render the subscriptions page:\n%s", librarySubscriptionsHref, firstN(dest, 800))
	}
	for _, want := range []string{"Add a subscription", "Download queue", "Activity"} {
		if !strings.Contains(dest, want) {
			t.Errorf("%s is missing %q — the page moved but lost part of itself", librarySubscriptionsHref, want)
		}
	}
}

// hrefsIn extracts every href="..." value from a fragment, in order.
func hrefsIn(s string) []string {
	var out []string
	const key = `href="`
	for {
		i := strings.Index(s, key)
		if i < 0 {
			return out
		}
		s = s[i+len(key):]
		j := strings.Index(s, `"`)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j:]
	}
}
