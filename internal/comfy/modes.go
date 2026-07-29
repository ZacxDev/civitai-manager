package comfy

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ── Multi-mode template workflows ────────────────────────────────────────────
//
// Some published "template pack" workflows ship SEVERAL parallel pipelines in one
// file — e.g. CivitAI model 1847730's "WAN 2.2 Smooth Workflow v6.0" carries
// TEXT2VIDEO / IMAGE2VIDEO / FIRST2LASTFRAME / AUDIO2VIDEO side by side, with 151
// of its 164 nodes bypassed. The author's intent is that the user enables exactly
// ONE pipeline in ComfyUI before running. Our converter drops bypassed/muted nodes,
// so the handful of survivors is not a runnable graph and the run aborts.
//
// DETECTION RULE (chosen deliberately — see the report in the commit message):
//
//	A mode selector is an ACTIVE rgthree "Fast Groups Bypasser" / "Fast Groups
//	Muter" node whose properties.toggleRestriction is "max one" or "always one",
//	controlling two or more groups.
//
// Why this rule and not "groups that are uniformly bypassed" or title keywords:
//
//   - It is the author's OWN declaration of mutual exclusivity. rgthree's
//     toggleRestriction is precisely "at most one of these groups may be enabled";
//     a pack author who wires a mode switch sets it, and one who wires independent
//     optional features leaves it at "default".
//   - It is structural: nothing is inferred from a group's title. Titles are used
//     only as the picker's LABELS.
//   - It discriminates on real data. Across the user's library: 581 (1847730) has
//     one "max one" bypasser controlling 4 groups; 588 (2184844) has an "always
//     one" bypasser over 3 model-loader groups AND a "max one" MUTER over 2 mask
//     groups; while 582/584/586/587 (pack 1386234) and 550 carry only "default"
//     togglers over 12-31 optional-feature groups each and are therefore correctly
//     NOT treated as multi-mode. A "uniformly bypassed group" heuristic would have
//     fired on all of those and mangled ordinary workflows.
//
// Group membership is resolved by LiteGraph's own geometry rule (the centre of a
// node's bounding rect inside the group's bounding), never by title.
//
// Everything here is EPHEMERAL: ApplyModeSelection returns a COPY, exactly like
// ApplyUIWidgetOverrides. The stored workflow is never mutated.

// fastGroupTogglerTypes are the rgthree node types that switch whole GROUPS on and
// off. (rgthree also ships "Fast Muter"/"Fast Bypasser", which target individual
// NODES rather than groups and carry no group matcher — they are not selectors.)
var fastGroupTogglerTypes = map[string]bool{
	"Fast Groups Bypasser (rgthree)": true,
	"Fast Groups Muter (rgthree)":    true,
}

// exclusiveRestrictions are the rgthree toggleRestriction values that declare the
// controlled groups MUTUALLY EXCLUSIVE. "default" (and anything else) does not.
var exclusiveRestrictions = map[string]bool{
	"max one":    true,
	"always one": true,
}

// litegraphGroupColors maps LiteGraph's palette NAMES (what rgthree stores in
// matchColors) to the hex a group's `color` actually carries. Values are lowercase;
// group colors are compared lowercased because graphs in the wild mix cases
// ("#8AA" vs "#8aa").
var litegraphGroupColors = map[string]string{
	"red":       "#a88",
	"brown":     "#b06634",
	"green":     "#8a8",
	"blue":      "#88a",
	"pale_blue": "#3f789e",
	"cyan":      "#8aa",
	"purple":    "#a1309b",
	"yellow":    "#b58b2a",
	"black":     "#444",
}

// ModeGroup is ONE selectable mode: a group the selector controls.
type ModeGroup struct {
	// Key is the opaque, stable identifier the UI round-trips ("<selector node
	// id>:<group index>"). It is validated against a re-detection on the way back
	// in, so a hostile value can only fail to match.
	Key string
	// Title is the group's title — UNTRUSTED author text, used only as a label and
	// escaped at render time. May be empty.
	Title string
	// Active reports whether this mode is the one currently enabled in the STORED
	// graph (every selectable node in it is at mode 0).
	Active bool
	// NodeCount is how many nodes selecting this mode would enable.
	NodeCount int
}

// ModeSelector is one mutually-exclusive group switch found in a UI graph.
type ModeSelector struct {
	// Key is the toggler node's id — stable for a stored graph.
	Key string
	// Label is the toggler node's title (UNTRUSTED; escaped at render time), or a
	// generic fallback when the author left it unnamed.
	Label string
	// OffMode is the mode value this selector's "off" state uses: 4 (bypass) for a
	// Fast Groups Bypasser, 2 (mute) for a Fast Groups Muter.
	OffMode int
	Modes   []ModeGroup
}

// Selected returns the Key of the mode currently enabled in the stored graph, or ""
// when none is (the 581 case — every pipeline bypassed, the user must pick).
func (s ModeSelector) Selected() string {
	for _, m := range s.Modes {
		if m.Active {
			return m.Key
		}
	}
	return ""
}

// ── internal parse shapes (defensive: everything that varies is RawMessage) ───

type modeGraphDoc struct {
	Nodes  []modeNode  `json:"nodes"`
	Groups []modeGroup `json:"groups"`
}

type modeNode struct {
	ID         json.RawMessage `json:"id"`
	Type       string          `json:"type"`
	Mode       int             `json:"mode"`
	Pos        json.RawMessage `json:"pos"`
	Size       json.RawMessage `json:"size"`
	Flags      json.RawMessage `json:"flags"`
	Properties struct {
		MatchColors       json.RawMessage `json:"matchColors"`
		MatchTitle        json.RawMessage `json:"matchTitle"`
		ToggleRestriction json.RawMessage `json:"toggleRestriction"`
	} `json:"properties"`
	Title string `json:"title"`
}

type modeGroup struct {
	Title    string          `json:"title"`
	Bounding []float64       `json:"bounding"`
	Color    json.RawMessage `json:"color"`
}

// rect is an axis-aligned box [x, y, w, h].
type rect struct{ x, y, w, h float64 }

func (r rect) contains(px, py float64) bool {
	return px >= r.x && px <= r.x+r.w && py >= r.y && py <= r.y+r.h
}

// strictlyContains reports whether r fully covers o AND is strictly larger, i.e. o
// is a NESTED sub-group of r rather than r itself.
func (r rect) strictlyContains(o rect) bool {
	return r.x <= o.x && r.y <= o.y &&
		r.x+r.w >= o.x+o.w && r.y+r.h >= o.y+o.h &&
		r.w*r.h > o.w*o.h
}

func (gr modeGroup) rect() (rect, bool) {
	if len(gr.Bounding) < 4 {
		return rect{}, false
	}
	return rect{gr.Bounding[0], gr.Bounding[1], gr.Bounding[2], gr.Bounding[3]}, true
}

// nodeTitleHeight is LiteGraph's NODE_TITLE_HEIGHT: a node's bounding rect starts
// one title bar ABOVE its stored pos, unless the node is collapsed.
const nodeTitleHeight = 30

// boundingRect reproduces LiteGraph's node bounding rect (pos, minus a title bar,
// by size plus that bar).
func (n modeNode) boundingRect() rect {
	x, y := numPair(n.Pos)
	w, hh := numPair(n.Size)
	th := float64(nodeTitleHeight)
	if n.collapsed() {
		th = 0
	}
	return rect{x, y - th, w, hh + th}
}

// centre is the point LiteGraph's containsCentre() test uses for group membership
// (LGraphGroup.recomputeInsideNodes). Using the CENTRE — not the corner, not an
// overlap test — is what makes nested and abutting groups partition cleanly.
func (n modeNode) centre() (float64, float64) {
	r := n.boundingRect()
	return r.x + r.w/2, r.y + r.h/2
}

func (n modeNode) collapsed() bool {
	if len(n.Flags) == 0 {
		return false
	}
	var f struct {
		Collapsed bool `json:"collapsed"`
	}
	_ = json.Unmarshal(n.Flags, &f)
	return f.Collapsed
}

// numPair reads a two-number JSON value that ComfyUI serializes either as an array
// ([x,y]) or, in some frontend versions, as an object ({"0":x,"1":y}).
func numPair(raw json.RawMessage) (float64, float64) {
	if len(raw) == 0 {
		return 0, 0
	}
	var arr []float64
	if err := json.Unmarshal(raw, &arr); err == nil {
		if len(arr) >= 2 {
			return arr[0], arr[1]
		}
		return 0, 0
	}
	var obj map[string]float64
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj["0"], obj["1"]
	}
	return 0, 0
}

// rawString reads a JSON value that SHOULD be a string but may be absent, null or
// (defensively) some other type — in which case it yields "".
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// ── detection ────────────────────────────────────────────────────────────────

// DetectModeSelectors finds the mutually-exclusive group switches in a UI-format
// graph. It returns nil for an api-format graph, an unparseable document, a graph
// with no groups, or — the overwhelmingly common case — an ordinary workflow whose
// author declared no exclusive switch. Callers MUST treat nil as "this is a normal
// workflow, change nothing".
func DetectModeSelectors(uiGraph json.RawMessage) []ModeSelector {
	doc, ok := parseModeGraph(uiGraph)
	if !ok {
		return nil
	}
	return detectSelectors(doc)
}

func parseModeGraph(uiGraph json.RawMessage) (*modeGraphDoc, bool) {
	if len(uiGraph) == 0 {
		return nil, false
	}
	var doc modeGraphDoc
	if err := json.Unmarshal(uiGraph, &doc); err != nil {
		return nil, false
	}
	if len(doc.Groups) == 0 || len(doc.Nodes) == 0 {
		return nil, false
	}
	return &doc, true
}

// togglerMatch is one fast-groups toggler and the group indices it controls.
type togglerMatch struct {
	nodeIdx int
	nodeID  string
	groups  []int
	off     int
	excl    bool
}

// detectSelectors is the shared core of detection and application: it resolves every
// fast-groups toggler to the groups it controls, then keeps the exclusive ones.
func detectSelectors(doc *modeGraphDoc) []ModeSelector {
	togglers := resolveTogglers(doc)

	var out []ModeSelector
	for _, t := range togglers {
		if !t.excl {
			continue
		}
		n := doc.Nodes[t.nodeIdx]
		// A bypassed/muted selector is not in play — the author switched the switch
		// itself off, so we do not present it as a mode picker.
		if isInactiveMode(n.Mode) {
			continue
		}
		modes := make([]ModeGroup, 0, len(t.groups))
		for _, gi := range t.groups {
			members := selectableMembers(doc, togglers, t, gi)
			if len(members) == 0 {
				continue // an empty (or fully sub-switched) group is not a selectable mode
			}
			active := true
			for _, ni := range members {
				if isInactiveMode(doc.Nodes[ni].Mode) {
					active = false
					break
				}
			}
			modes = append(modes, ModeGroup{
				Key:       t.nodeID + ":" + strconv.Itoa(gi),
				Title:     doc.Groups[gi].Title,
				Active:    active,
				NodeCount: len(members),
			})
		}
		if len(modes) < 2 {
			continue // a single option is not a choice
		}
		label := strings.TrimSpace(n.Title)
		if label == "" {
			label = "Mode"
		}
		out = append(out, ModeSelector{Key: t.nodeID, Label: label, OffMode: t.off, Modes: modes})
	}
	return out
}

// resolveTogglers finds every fast-groups toggler node and the group indices it
// matches, in graph order (so keys and picker order are stable for a stored graph).
func resolveTogglers(doc *modeGraphDoc) []togglerMatch {
	var out []togglerMatch
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if !fastGroupTogglerTypes[n.Type] {
			continue
		}
		id := idToString(n.ID)
		if id == "" {
			continue
		}
		off := modeBypass
		if n.Type == "Fast Groups Muter (rgthree)" {
			off = modeMuted
		}
		out = append(out, togglerMatch{
			nodeIdx: i,
			nodeID:  id,
			groups:  matchedGroupIndices(doc, n),
			off:     off,
			excl:    exclusiveRestrictions[strings.ToLower(strings.TrimSpace(rawString(n.Properties.ToggleRestriction)))],
		})
	}
	return out
}

// matchedGroupIndices applies the toggler's matchColors + matchTitle filters, the
// same way rgthree does: matchColors is a comma-separated list of LiteGraph palette
// NAMES compared against the group's color, and matchTitle is a case-insensitive
// REGEX over the group's title. An empty filter matches everything.
func matchedGroupIndices(doc *modeGraphDoc, n *modeNode) []int {
	colors := map[string]bool{}
	for _, c := range strings.Split(rawString(n.Properties.MatchColors), ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if hex, ok := litegraphGroupColors[c]; ok {
			colors[hex] = true
		} else {
			colors[c] = true // tolerate a raw hex stored in matchColors
		}
	}

	title := strings.TrimSpace(rawString(n.Properties.MatchTitle))
	var re *regexp.Regexp
	if title != "" {
		// rgthree builds `new RegExp(matchTitle, 'i')`. Go's RE2 rejects some JS
		// patterns (and Go has no backtracking, so this cannot blow up); when it does
		// not compile, fall back to a case-insensitive substring — which is what every
		// matchTitle observed in real packs ("#", "::", "!", "📷") means anyway.
		re, _ = regexp.Compile("(?i)" + title)
	}

	var out []int
	for gi := range doc.Groups {
		gr := &doc.Groups[gi]
		if len(colors) > 0 && !colors[strings.ToLower(rawString(gr.Color))] {
			continue
		}
		if title != "" {
			if re != nil {
				if !re.MatchString(gr.Title) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(gr.Title), strings.ToLower(title)) {
				continue
			}
		}
		if _, ok := gr.rect(); !ok {
			continue // malformed bounding — no resolvable membership
		}
		out = append(out, gi)
	}
	return out
}

// selectableMembers returns the indices of the nodes a selector would switch for
// group gi: every node whose bounding-rect CENTRE falls inside the group, MINUS any
// node that also falls inside a sub-group strictly nested within it that a DIFFERENT
// toggler controls.
//
// That subtraction is what keeps a mode switch from stomping an independent
// sub-switch: in 581 each pipeline group contains its own "<X> AUDIO" sub-group
// driven by a separate, non-exclusive "Audio Enabler" bypasser. The author's default
// for those is off, and enabling the pipeline must not silently turn them on. It is
// applied symmetrically to the enable and the disable side, so a sub-group's stored
// mode always survives a mode switch.
func selectableMembers(doc *modeGraphDoc, togglers []togglerMatch, self togglerMatch, gi int) []int {
	gr, ok := doc.Groups[gi].rect()
	if !ok {
		return nil
	}
	var nested []rect
	for _, t := range togglers {
		if t.nodeID == self.nodeID {
			continue
		}
		for _, ogi := range t.groups {
			or, ok := doc.Groups[ogi].rect()
			if ok && gr.strictlyContains(or) {
				nested = append(nested, or)
			}
		}
	}

	var out []int
	for ni := range doc.Nodes {
		cx, cy := doc.Nodes[ni].centre()
		if !gr.contains(cx, cy) {
			continue
		}
		skip := false
		for _, nr := range nested {
			if nr.contains(cx, cy) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, ni)
		}
	}
	return out
}

// ── application ──────────────────────────────────────────────────────────────

// ApplyModeSelection returns a COPY of a UI-format graph with each chosen mode's
// nodes enabled (mode 0) and its selector's OTHER modes' nodes set to that
// selector's off mode — the "max one" semantics the author declared. choices maps a
// ModeSelector.Key to one of that selector's ModeGroup.Keys.
//
// Discipline (identical to ApplyUIWidgetOverrides):
//
//   - the input graph is NEVER mutated, and when nothing actually changes the input
//     bytes are returned unchanged (so a workflow with no selectors, an empty
//     choice set, or a choice that is already in effect is byte-identical);
//   - an unknown selector key, a mode key that belongs to a DIFFERENT selector, and
//     a malformed key are all ignored — a caller can only ever pick a real mode of
//     a real selector, because the accepted set is re-derived from the graph here;
//   - only a node's `mode` field is touched. Nothing is added or removed.
//
// If two modes of one selector overlap geometrically, the SELECTED mode wins: the
// disable pass runs first and the enable pass second.
func ApplyModeSelection(uiGraph json.RawMessage, choices map[string]string) json.RawMessage {
	if len(choices) == 0 {
		return uiGraph
	}
	doc, ok := parseModeGraph(uiGraph)
	if !ok {
		return uiGraph
	}
	selectors := detectSelectors(doc)
	if len(selectors) == 0 {
		return uiGraph
	}
	togglers := resolveTogglers(doc)

	// target[nodeIdx] = the mode to force. Disable first, enable second, so a node
	// shared by two modes of one selector ends up enabled.
	disable := map[int]int{}
	enable := map[int]bool{}
	for _, sel := range selectors {
		want, ok := choices[sel.Key]
		if !ok || want == "" {
			continue
		}
		var self togglerMatch
		for _, t := range togglers {
			if t.nodeID == sel.Key {
				self = t
				break
			}
		}
		known := false
		for _, m := range sel.Modes {
			if m.Key == want {
				known = true
				break
			}
		}
		if !known {
			continue // unknown / cross-selector / hostile mode key
		}
		for _, m := range sel.Modes {
			gi, err := strconv.Atoi(strings.TrimPrefix(m.Key, sel.Key+":"))
			if err != nil {
				continue
			}
			members := selectableMembers(doc, togglers, self, gi)
			if m.Key == want {
				for _, ni := range members {
					enable[ni] = true
				}
			} else {
				for _, ni := range members {
					disable[ni] = sel.OffMode
				}
			}
		}
	}
	if len(disable) == 0 && len(enable) == 0 {
		return uiGraph
	}

	final := map[int]int{}
	for ni, m := range disable {
		final[ni] = m
	}
	for ni := range enable {
		final[ni] = modeNormal
	}
	return rewriteNodeModes(uiGraph, doc, final)
}

// rewriteNodeModes re-serializes the graph with the given per-node-index mode
// values, preserving every other field verbatim (nodes are re-marshaled from their
// own raw JSON objects, not from the typed parse). Returns the input unchanged when
// no node's mode actually differs.
func rewriteNodeModes(uiGraph json.RawMessage, doc *modeGraphDoc, final map[int]int) json.RawMessage {
	changed := false
	for ni, want := range final {
		if ni < len(doc.Nodes) && doc.Nodes[ni].Mode != want {
			changed = true
			break
		}
	}
	if !changed {
		return uiGraph
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(uiGraph, &top); err != nil {
		return uiGraph
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(top["nodes"], &nodes); err != nil {
		return uiGraph
	}
	for ni, want := range final {
		if ni >= len(nodes) {
			continue
		}
		var node map[string]json.RawMessage
		if err := json.Unmarshal(nodes[ni], &node); err != nil {
			continue
		}
		node["mode"] = json.RawMessage(strconv.Itoa(want))
		nb, err := json.Marshal(node)
		if err != nil {
			continue
		}
		nodes[ni] = nb
	}
	nb, err := json.Marshal(nodes)
	if err != nil {
		return uiGraph
	}
	top["nodes"] = nb
	out, err := json.Marshal(top)
	if err != nil {
		return uiGraph
	}
	return out
}
