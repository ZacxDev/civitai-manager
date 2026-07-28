package web

import (
	"strings"
	"testing"
)

// TestNavbarFlatRename proves the top nav uses the disambiguated labels "Models"
// (for /search) and "Workflows" (for /workflows/discover) while keeping the routes
// unchanged, and that the old ambiguous "Search"/"Discover" labels are gone.
func TestNavbarFlatRename(t *testing.T) {
	body := renderString(t, navbar("dark", "csrf-token", "blur", railData{}))

	// New labels present.
	if !strings.Contains(body, ">Models<") {
		t.Errorf("nav should contain the 'Models' label:\n%s", body)
	}
	if !strings.Contains(body, ">Workflows<") {
		t.Errorf("nav should contain the 'Workflows' label:\n%s", body)
	}

	// Routes unchanged.
	if !strings.Contains(body, `href="/search"`) {
		t.Errorf("the Models link must still point at /search")
	}
	if !strings.Contains(body, `href="/workflows/discover"`) {
		t.Errorf("the Workflows link must still point at /workflows/discover")
	}

	// Old ambiguous labels gone (guard against the exact anchor text, not the
	// "Search models" heading which lives on the page body, not this nav fragment).
	if strings.Contains(body, ">Search<") {
		t.Errorf("nav should no longer render the old 'Search' label:\n%s", body)
	}
	if strings.Contains(body, ">Discover<") {
		t.Errorf("nav should no longer render the old 'Discover' label:\n%s", body)
	}

	// Unchanged siblings still present.
	for _, label := range []string{">Dashboard<", ">Apps<", ">Library<", ">Trash<", ">civitai-manager<"} {
		if !strings.Contains(body, label) {
			t.Errorf("nav lost an expected label %q", label)
		}
	}
}
