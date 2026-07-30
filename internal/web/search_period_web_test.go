package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// The period dropdown offers exactly CivitAI's `period` enum, narrowest → widest.
// CivitAI answers HTTP 400 to anything outside it — verified live against
// /api/v1/models: Day/Week/Month/Year/AllTime → 200, ThreeMonths → 400 — so a
// 3-month or 6-month window cannot be offered, under any spelling.
var wantPeriodOrder = []struct {
	value string
	label string
}{
	{"Day", "Today"},
	{"Week", "This week"},
	{"Month", "This month"},
	{"Year", "This year"},
	{"AllTime", "All time"},
}

// TestSearchPeriodOptionsAreTheCivitAIEnum pins the table itself: the exact five
// values, in the intended order, and nothing that upstream would reject.
func TestSearchPeriodOptionsAreTheCivitAIEnum(t *testing.T) {
	if len(searchPeriodOptions) != len(wantPeriodOrder) {
		t.Fatalf("searchPeriodOptions has %d entries, want %d: %+v",
			len(searchPeriodOptions), len(wantPeriodOrder), searchPeriodOptions)
	}
	for i, want := range wantPeriodOrder {
		got := searchPeriodOptions[i]
		if got.Value != want.value || got.Label != want.label {
			t.Errorf("searchPeriodOptions[%d] = {%q, %q}, want {%q, %q}",
				i, got.Value, got.Label, want.value, want.label)
		}
	}
	// The windows CivitAI does NOT implement must never appear, in any spelling.
	for _, o := range searchPeriodOptions {
		switch o.Value {
		case "ThreeMonths", "SixMonths", "Quarter", "3 Months", "6 Months":
			t.Errorf("period %q is not in CivitAI's enum — the API answers 400", o.Value)
		}
	}
}

// TestPeriodSelectRendersAllOptionsInOrder proves BOTH surfaces that share
// searchPeriodOptions actually pick the widened set up, and render it in order —
// the model search and the workflow discover page.
func TestPeriodSelectRendersAllOptionsInOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"model search", "/search?q=x"},
		{"model search feed", "/search"},
		{"workflow discover", "/workflows/discover"},
		{"workflow discover search", "/workflows/discover?q=wan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &recordingSearchReader{result: workflowResult(t)}
			srv := newModelServer(t, reader)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", tc.target, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `name="period"`) {
				t.Fatalf("no period select on %s", tc.target)
			}
			// Every option is present, and each appears AFTER the previous one, so the
			// select reads narrowest → widest.
			prev := -1
			for _, want := range wantPeriodOrder {
				// The selected option renders as `value="X" selected`, so match the
				// value attribute alone and assert the label separately.
				at := strings.Index(body, `<option value="`+want.value+`"`)
				if at < 0 {
					t.Errorf("period select is missing option %q", want.value)
					continue
				}
				if !strings.Contains(body, `>`+want.label+`</option>`) {
					t.Errorf("period option %q is missing its label %q", want.value, want.label)
				}
				if at < prev {
					t.Errorf("period option %q renders out of order (offset %d < %d)",
						want.value, at, prev)
				}
				prev = at
			}
		})
	}
}

// TestHostilePeriodIsNeverForwarded is the whitelist requirement: an unknown or
// hostile ?period= is normalized to the surface's default and the raw value never
// reaches civitai.com. A value outside CivitAI's enum would be an HTTP 400.
func TestHostilePeriodIsNeverForwarded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  string
		wantVal string
	}{
		{"sql-ish", "/search?q=x&period=DROP+TABLE", "AllTime"},
		{"upstream-400 window", "/search?q=x&period=ThreeMonths", "AllTime"},
		{"comma-joined", "/search?q=x&period=Month,Year", "AllTime"},
		{"case-wrong", "/search?q=x&period=year", "AllTime"},
		{"empty", "/search?q=x&period=", "AllTime"},
		{"valid passes through", "/search?q=x&period=Year", "Year"},
		{"valid Day passes through", "/search?q=x&period=Day", "Day"},
		// The empty-query feed's default is Month (the cached popular window).
		{"feed default", "/search?period=SixMonths", "Month"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &recordingSearchReader{result: &civitai.ModelSearchResult{}}
			srv := newModelServer(t, reader)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", tc.target, rec.Code)
			}
			reader.mu.Lock()
			if len(reader.calls) == 0 {
				reader.mu.Unlock()
				t.Fatal("no upstream call recorded — the assertion below would false-pass")
			}
			q := reader.calls[len(reader.calls)-1]
			reader.mu.Unlock()
			if got := q.Get("period"); got != tc.wantVal {
				t.Errorf("forwarded period = %q, want %q", got, tc.wantVal)
			}
		})
	}
}

// TestNormalizeSearchPeriodTable covers the unit directly, including that the
// default is returned verbatim (callers pass the surface's own default).
func TestNormalizeSearchPeriodTable(t *testing.T) {
	for _, tc := range []struct {
		in, def, want string
	}{
		{"Day", "AllTime", "Day"},
		{"Week", "AllTime", "Week"},
		{"Month", "AllTime", "Month"},
		{"Year", "AllTime", "Year"},
		{"AllTime", "Month", "AllTime"},
		{"", "Month", "Month"},
		{"ThreeMonths", "Month", "Month"},
		{"SixMonths", "AllTime", "AllTime"},
		{"3 Months", "Month", "Month"},
		{"alltime", "Month", "Month"},
		{"../../etc/passwd", "Month", "Month"},
	} {
		if got := normalizeSearchPeriod(tc.in, tc.def); got != tc.want {
			t.Errorf("normalizeSearchPeriod(%q, %q) = %q, want %q", tc.in, tc.def, got, tc.want)
		}
	}
}

// TestDiscoverDefaultPeriodUnchangedByWiderSet — widening the option set must not
// move either surface's default: a browse stays on Month ("Popular this month",
// the cached feed) and a keyword search stays on AllTime.
func TestDiscoverDefaultPeriodUnchangedByWiderSet(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"", "Month"},
		{"   ", "Month"},
		{"wan", "AllTime"},
		{" wan ", "AllTime"},
	} {
		if got := discoverDefaultPeriod(tc.query); got != tc.want {
			t.Errorf("discoverDefaultPeriod(%q) = %q, want %q", tc.query, got, tc.want)
		}
		// Whatever the default is, it must itself be a whitelisted option — a default
		// outside the enum would be forwarded and 400.
		if normalizeSearchPeriod(tc.want, "sentinel") != tc.want {
			t.Errorf("default period %q is not in searchPeriodOptions", tc.want)
		}
	}
}

// TestDiscoverHrefOmitsOnlyTheDefaultPeriod — the widened set must not make a
// canonical URL drop a period the user actually chose (discoverHref omits only the
// default, and that default is query-dependent).
func TestDiscoverHrefOmitsOnlyTheDefaultPeriod(t *testing.T) {
	for _, tc := range []struct{ query, period, want string }{
		{"", "Month", "/workflows/discover"},                       // browse default → omitted
		{"", "Year", "/workflows/discover?period=Year"},            // chosen → kept
		{"", "Day", "/workflows/discover?period=Day"},              // chosen → kept
		{"wan", "AllTime", "/workflows/discover?q=wan"},            // search default → omitted
		{"wan", "Year", "/workflows/discover?period=Year&q=wan"},   // chosen → kept
		{"wan", "Month", "/workflows/discover?period=Month&q=wan"}, // chosen → kept
	} {
		if got := discoverHref(tc.query, "", tc.period, "", ""); got != tc.want {
			t.Errorf("discoverHref(q=%q, period=%q) = %q, want %q", tc.query, tc.period, got, tc.want)
		}
	}
}

// TestPeriodPhraseCoversEveryOption — the facet empty-state heading names the
// window ("No Flux.1 workflows this year"). Every option except AllTime must have
// a phrase, or widening the set leaves the new windows silently unnamed.
func TestPeriodPhraseCoversEveryOption(t *testing.T) {
	for _, o := range searchPeriodOptions {
		got := periodPhrase(o.Value)
		if o.Value == "AllTime" {
			if got != "" {
				t.Errorf("periodPhrase(AllTime) = %q, want \"\" (nothing to widen to)", got)
			}
			continue
		}
		if got == "" {
			t.Errorf("periodPhrase(%q) is empty — the empty-state heading would not name the window", o.Value)
		}
	}
	if got := periodPhrase("ThreeMonths"); got != "" {
		t.Errorf("periodPhrase of a non-option = %q, want \"\"", got)
	}
}
