package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// ---------------------------------------------------------------------------
// Version-tab bucketing: the newest N as tabs, the rest behind ONE disclosure.
// ---------------------------------------------------------------------------
// /models/4384 ships 31 versions and rendered a seven-row tab wall. Only the
// newest versionTabVisibleN — plus the SELECTED version, always — stay as plain
// tabs now; everything else folds into a <details>.
//
// The fixture below is the whole point of these tests: its ARRAY order and its
// publishedAt order deliberately DISAGREE. modelVersions[] is ordered by the
// creator's `index` (primary first), so an implementation that took vers[:N]
// positionally would look correct against any same-order fixture and ship the
// documented ship-then-revert bug. Here, taking the first N positionally keeps
// exactly the versions this test asserts must be HIDDEN.

// invertedDateVersions builds n versions whose array order is the exact REVERSE
// of their publish order: vers[0] is the OLDEST (the "primary" slot), vers[n-1]
// the newest. IDs and names are 1..n in array order.
func invertedDateVersions(n int) ([]civitai.ModelVersionSummary, map[int]time.Time) {
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	vers := make([]civitai.ModelVersionSummary, 0, n)
	dates := make(map[int]time.Time, n)
	for i := 0; i < n; i++ {
		id := i + 1
		vers = append(vers, civitai.ModelVersionSummary{
			ID: id, Name: fmt.Sprintf("v%d", id), BaseModel: "SD 1.5",
		})
		// Later array position => LATER publish date, so vers[0] is oldest.
		dates[id] = base.AddDate(0, 0, i)
	}
	return vers, dates
}

func bucketView(sel int, vers []civitai.ModelVersionSummary, dates map[int]time.Time) modelDetailView {
	return modelDetailView{
		Model:              &civitai.ModelDetail{ID: 7, Name: "M", ModelVersions: vers},
		SelectedVersionID:  sel,
		LocalVersionIDs:    map[int]bool{},
		VersionPublishedAt: dates,
	}
}

var versionTabHrefRE = regexp.MustCompile(`href="/models/7\?version=(\d+)"`)

// visibleTabIDs returns the version ids rendered as PLAIN tabs (outside the
// disclosure), in document order. It splits on the <details> so a version folded
// into the disclosure can never be mistaken for a visible tab — the difference
// this whole feature turns on.
func visibleTabIDs(t *testing.T, html string) []int {
	t.Helper()
	head := html
	if i := strings.Index(html, "<details"); i >= 0 {
		head = html[:i]
	}
	return tabIDsIn(t, head)
}

// hiddenTabIDs returns the version ids inside the disclosure, in document order.
func hiddenTabIDs(t *testing.T, html string) []int {
	t.Helper()
	i := strings.Index(html, "<details")
	if i < 0 {
		return nil
	}
	return tabIDsIn(t, html[i:])
}

func tabIDsIn(t *testing.T, frag string) []int {
	t.Helper()
	var out []int
	for _, m := range versionTabHrefRE.FindAllStringSubmatch(frag, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparseable version id %q", m[1])
		}
		out = append(out, n)
	}
	return out
}

func sameIDSet(got []int, want ...int) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[int]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

// TestVersionTabsBucketByPublishedAtNotArrayOrder is the ship-then-revert guard.
// Array order is v1..v10; publish order is the exact reverse. The visible strip
// must be the newest six (v5..v10) — NOT the first six in the array.
func TestVersionTabsBucketByPublishedAtNotArrayOrder(t *testing.T) {
	vers, dates := invertedDateVersions(10)
	// Select the newest so the "always visible" rule cannot mask a bad ranking.
	out := renderString(t, modelVersionTabs(bucketView(10, vers, dates)))

	vis := visibleTabIDs(t, out)
	if !sameIDSet(vis, 5, 6, 7, 8, 9, 10) {
		t.Errorf("visible tabs must be the six NEWEST by publishedAt (v5..v10), got %v.\n"+
			"Array order is v1..v10 and publish order is its reverse, so %v means the "+
			"split read modelVersions[] POSITIONALLY — the documented ship-then-revert "+
			"bug (vers[0] is the creator's PRIMARY version, not the latest).\n%s",
			vis, vis, out)
	}
	hid := hiddenTabIDs(t, out)
	if !sameIDSet(hid, 1, 2, 3, 4) {
		t.Errorf("the disclosure must hold the four OLDEST (v1..v4), got %v:\n%s", hid, out)
	}
}

// TestVersionTabsDisclosureReportsHiddenCount pins the count in the summary text
// (not only in an aria-label) and the <details>/<summary> shape the keyboard
// affordance depends on.
func TestVersionTabsDisclosureReportsHiddenCount(t *testing.T) {
	vers, dates := invertedDateVersions(31)
	out := renderString(t, modelVersionTabs(bucketView(31, vers, dates)))

	if got := len(visibleTabIDs(t, out)); got != versionTabVisibleN {
		t.Errorf("expected %d visible tabs, got %d:\n%s", versionTabVisibleN, got, out)
	}
	wantHidden := 31 - versionTabVisibleN
	if got := len(hiddenTabIDs(t, out)); got != wantHidden {
		t.Errorf("expected %d tabs inside the disclosure, got %d", wantHidden, got)
	}
	if !strings.Contains(out, fmt.Sprintf(">%d older<", wantHidden)) {
		t.Errorf("the disclosure must state the hidden COUNT (%d older) in its visible summary text:\n%s",
			wantHidden, out)
	}
	// Keyboard operability comes from the native element — no JS, no click handler.
	for _, want := range []string{"<details", "<summary", `class="cm-vmore"`, `class="cm-vmore-sum"`} {
		if !strings.Contains(out, want) {
			t.Errorf("disclosure must be a native <details> (keyboard-operable with no JS); missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "onclick=\"cmVMore") {
		t.Errorf("the disclosure must not grow a JS toggle — <details> already does this:\n%s", out)
	}
}

// TestVersionTabsSelectedIsAlwaysVisible: selecting a genuinely OLD version must
// not make the active tab disappear into the collapsed disclosure.
func TestVersionTabsSelectedIsAlwaysVisible(t *testing.T) {
	vers, dates := invertedDateVersions(10)
	for _, sel := range []int{1, 2, 3, 4, 5, 10} {
		t.Run("v"+strconv.Itoa(sel), func(t *testing.T) {
			out := renderString(t, modelVersionTabs(bucketView(sel, vers, dates)))
			vis := visibleTabIDs(t, out)
			found := false
			for _, id := range vis {
				if id == sel {
					found = true
				}
			}
			if !found {
				t.Fatalf("selected version v%d is NOT among the visible tabs %v — it was folded "+
					"into the collapsed disclosure, so the strip shows no active tab at all:\n%s",
					sel, vis, out)
			}
			// It must be the ACTIVE tab, not merely present.
			if !strings.Contains(out, "cm-version-tab cm-version-tab-active") {
				t.Errorf("no active tab rendered for selection v%d:\n%s", sel, out)
			}
			// Forcing the selection in must DISPLACE, not grow the strip.
			if len(vis) != versionTabVisibleN {
				t.Errorf("visible strip must stay %d tabs when the selection is forced in, got %d (%v)",
					versionTabVisibleN, len(vis), vis)
			}
			// And it must displace the OLDEST kept version, never a newer one: with
			// an old selection the strip is the selection plus the newest N-1.
			if sel <= 10-versionTabVisibleN {
				if !sameIDSet(vis, sel, 6, 7, 8, 9, 10) {
					t.Errorf("selecting old v%d should displace only the oldest kept version; got %v", sel, vis)
				}
			}
		})
	}
}

// TestVersionTabsNoDisclosureBelowThreshold: at or under the limit nothing folds,
// so short models render exactly as they always did.
func TestVersionTabsNoDisclosureBelowThreshold(t *testing.T) {
	for _, n := range []int{1, 3, versionTabVisibleN} {
		vers, dates := invertedDateVersions(n)
		out := renderString(t, modelVersionTabs(bucketView(1, vers, dates)))
		if strings.Contains(out, "cm-vmore") {
			t.Errorf("%d versions is at/below versionTabVisibleN=%d — no disclosure should render:\n%s",
				n, versionTabVisibleN, out)
		}
		if got := len(visibleTabIDs(t, out)); got != n {
			t.Errorf("expected all %d versions as plain tabs, got %d", n, got)
		}
	}
}

// TestVersionTabsUndatedVersionsRankLast: a version with no parseable publishedAt
// cannot be claimed as recent, so it folds before any dated one does.
func TestVersionTabsUndatedVersionsRankLast(t *testing.T) {
	vers, dates := invertedDateVersions(8)
	// Strip the dates off the two NEWEST by array position (v7, v8). They must now
	// fold, even though a positional read would keep them.
	delete(dates, 7)
	delete(dates, 8)
	out := renderString(t, modelVersionTabs(bucketView(1, vers, dates)))
	hid := hiddenTabIDs(t, out)
	for _, want := range []int{7, 8} {
		found := false
		for _, id := range hid {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("undated v%d must rank LAST and fold into the disclosure; hidden set was %v:\n%s",
				want, hid, out)
		}
	}
}

// TestVersionTabsGroupedPathBucketsPerGroup: the base-model grouping still
// applies, and bucketing happens INSIDE each group's panel rather than across
// the whole model (a group of 3 must not be folded away by a sibling of 30).
func TestVersionTabsGroupedPathBucketsPerGroup(t *testing.T) {
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	var vers []civitai.ModelVersionSummary
	dates := map[int]time.Time{}
	// 10 × "SD 1.5" (ids 1..10) then 3 × "SDXL" (ids 11..13).
	for i := 1; i <= 13; i++ {
		bm := "SD 1.5"
		if i > 10 {
			bm = "SDXL"
		}
		vers = append(vers, civitai.ModelVersionSummary{ID: i, Name: fmt.Sprintf("v%d", i), BaseModel: bm})
		dates[i] = base.AddDate(0, 0, i)
	}
	out := renderString(t, modelVersionTabs(bucketView(10, vers, dates)))

	if !strings.Contains(out, "cm-vgroup-pill") {
		t.Fatalf("the base-model grouping must survive bucketing:\n%s", out)
	}
	// Exactly ONE disclosure: the SD 1.5 group (10 > 6). The SDXL group (3) has none.
	if got := strings.Count(out, `class="cm-vmore"`); got != 1 {
		t.Errorf("expected exactly 1 disclosure (only the 10-version group needs one), got %d:\n%s", got, out)
	}
	if !strings.Contains(out, ">4 older<") {
		t.Errorf("the SD 1.5 group should fold 10-%d=4 versions:\n%s", versionTabVisibleN, out)
	}
	// The 3-version SDXL group keeps all three as plain tabs.
	for _, id := range []int{11, 12, 13} {
		if !strings.Contains(out, `href="/models/7?version=`+strconv.Itoa(id)+`"`) {
			t.Errorf("version %d (small group) must still render a tab:\n%s", id, out)
		}
	}
}
