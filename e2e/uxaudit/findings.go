package uxaudit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file turns a ViewCapture's already-captured raw signals (the axe result JSON
// + the origin-classified console/network events) into auditloop push FINDINGS.
//
// WHY it exists: the push used to carry only the per-page ROLL-UP COUNTS
// (axe_violations, console_first_party, …) and never populated PushPage.Findings.
// Three things broke as a result on the receiving auditloop instance:
//
//  1. The run report's exec summary derives "no accessibility issues" from stored
//     a11y FINDINGS, so a run with real axe violations still reported clean.
//  2. auditloop's P2 a11y-rule delta (`new_a11y_rules`) — what a CI
//     `--fail-on-regression` gate keys on — reads a TOP-LEVEL "id" out of each
//     stored a11y finding's detail. With no findings, that delta is permanently
//     empty and the a11y regression gate is a silent no-op.
//  3. The captured console/network error TEXT was discarded (only counts survived),
//     so you could see "1 first-party console error" but never what it said.
//
// SCOPE (deliberate omissions):
//   - Only FIRST-PARTY console/network events become findings. Third-party events
//     (analytics/CDNs) stay bucketed in the *_third_party counts — they are
//     environmental, not this app's UX defects, and auditloop never drops them.
//   - NO perf/layout findings are emitted. auditloop computes those SERVER-SIDE from
//     the optional raw perf/layout blocks (internal/signals, one source of truth for
//     the thresholds), and this harness pushes environment:"lab" anyway, for which
//     auditloop suppresses perf findings as not field-representative.
//   - The roll-up count fields are untouched: findings are ADDITIVE, and
//     axe_violations keeps its existing meaning (number of violated RULES).

// Finding-emission caps. The push is already size-capped end to end (16 MiB per
// part / 64 MiB total, self-checked by ValidateFiles), but the metadata part is a
// single JSON blob whose size we control here — so a pathological page (hundreds of
// contrast violations, a console error loop) must not be able to inflate it. Every
// cap TRUNCATES rather than failing: a bounded, honest finding set beats no push.
const (
	// maxA11yFindings caps emitted a11y findings (one per violated RULE; axe ships
	// ~100 rules, so this is generous headroom, not a routine trim).
	maxA11yFindings = 120
	// maxConsoleFindings / maxNetworkFindings cap per-page first-party event findings
	// (a runaway error loop can emit thousands of identical console errors).
	maxConsoleFindings = 25
	maxNetworkFindings = 25

	// maxA11yNodes caps the example nodes kept per rule. The axe script already
	// slices to 5 (axe.go axeRunScript); re-capping here keeps the bound in Go so it
	// holds even for an axe payload produced elsewhere.
	maxA11yNodes = 3
	// maxNodeTargets caps selectors per example node (axe emits a frame-path array).
	maxNodeTargets = 4

	// String caps. maxNodeHTMLLen matches the axe script's own 300-char slice.
	maxNodeHTMLLen   = 300
	maxSelectorLen   = 200
	maxHelpTextLen   = 300
	maxRuleIDLen     = 128
	maxDetailTextLen = 500
	maxURLLen        = 300
	// maxImpactLen caps the axe impact string (critical|serious|moderate|minor), which
	// is emitted BOTH as the finding severity and inside the detail.
	maxImpactLen = 32
	// maxA11yTags caps the axe tag list (wcag2aa, cat.color, …); maxTagLen caps each.
	maxA11yTags = 12
	maxTagLen   = 64
)

// Finding type + severity values. Types are the closed set auditloop's Validate
// accepts (mirrored in push.go); severities are free text server-side.
const (
	FindingA11y    = "a11y"
	FindingConsole = "console"
	FindingNetwork = "network"

	severityInfo     = "info"
	severityModerate = "moderate"
	severitySerious  = "serious"
)

// axeResult is the shape axeRunScript (axe.go) returns.
//
// Error is load-bearing, not decoration: axeRunScript catches a thrown exception and
// returns {"error":"...","violations":[]}. Modelling no Error field made an axe scan
// that NEVER RAN indistinguishable from a genuinely clean page. That was cosmetic
// while only roll-up counts were pushed; once findings drive auditloop's a11y rule
// set it is a correctness hole: one errored scan empties the run's rule set, so
// `resolved_a11y_rules` reports EVERY rule as fixed and the next good run reports
// every rule as NEW — a spurious regression-gate failure on a report that
// simultaneously claims "no accessibility issues". A scan that didn't run is not a
// passing audit, so it is surfaced as an error and fails the walk loudly.
type axeResult struct {
	Error      string         `json:"error,omitempty"`
	Violations []axeViolation `json:"violations"`
}

// ErrAxeScanFailed reports that axe-core itself failed on a page (it threw, so the
// page was never actually scanned). It is deliberately FATAL to a walk rather than
// degrading: a silently-unscanned page reads as "clean" and corrupts the a11y rule
// set auditloop diffs run to run.
type ErrAxeScanFailed struct {
	Reason string
}

func (e *ErrAxeScanFailed) Error() string {
	return fmt.Sprintf("axe scan failed (the page was NOT scanned; treating as clean would corrupt the a11y regression baseline): %s", e.Reason)
}

// axeViolation is one violated axe RULE, mirroring the object axeRunScript emits.
// Its json tags are also the SHAPE THAT GOES OVER THE WIRE as an a11y finding's
// detail (see A11yFindings) — the raw-axe-violation shape auditloop's crawl path
// stores natively and whose top-level `id` the P2 rule delta extracts. Renaming
// `id` here silently disables the a11y regression gate, so it is asserted by test.
type axeViolation struct {
	ID        string    `json:"id"`
	Impact    string    `json:"impact,omitempty"`
	Help      string    `json:"help,omitempty"`
	HelpURL   string    `json:"helpUrl,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	NodeCount int       `json:"nodeCount"`
	Nodes     []axeNode `json:"nodes,omitempty"`
}

// axeNode is one example offending element.
type axeNode struct {
	Target []string `json:"target,omitempty"`
	HTML   string   `json:"html,omitempty"`
}

// A11yFindings maps a raw axe result to one PushFinding per violated RULE (NOT per
// offending node — a page with 61 color-contrast nodes yields ONE color-contrast
// finding, matching how axe_violations counts rules and how auditloop's rule-set
// delta reasons).
//
// The detail is the axe violation object marshaled to JSON and carried in the
// string-typed PushFinding.Detail. That encoding is load-bearing: auditloop's
// plugin.a11yDetail unmarshals the detail STRING, and if it parses as an object with
// a non-empty top-level "id" it stores those bytes VERBATIM as the finding detail —
// which is exactly what makes the P2 `new_a11y_rules` delta (and therefore the CI
// a11y regression gate) work for pushed runs. A plain free-text detail would instead
// hit auditloop's legacy fallback, so the JSON-object encoding is required, not
// stylistic.
//
// A violation with a BLANK rule id is SKIPPED rather than emitted: it could not
// serve as a stable gate key, and auditloop's legacy fallback would derive a
// meaningless id from the JSON text.
//
// It returns an *ErrAxeScanFailed when the axe result reports a scan error, and an
// error when the axe JSON is unparseable — in BOTH cases the page was not reliably
// scanned, and returning "no findings" would let it read as CLEAN and poison the
// a11y regression baseline. An EMPTY axeJSON is not treated as an error (there is
// nothing to interpret); the walk asserts separately that every capture produced axe
// output, so an absent scan still fails loudly there.
func A11yFindings(axeJSON []byte) ([]PushFinding, error) {
	if len(axeJSON) == 0 {
		return nil, nil
	}
	var res axeResult
	if err := json.Unmarshal(axeJSON, &res); err != nil {
		return nil, fmt.Errorf("axe result is not valid JSON (page not reliably scanned; treating as clean would corrupt the a11y regression baseline): %w", err)
	}
	if strings.TrimSpace(res.Error) != "" {
		return nil, &ErrAxeScanFailed{Reason: truncateStr(strings.TrimSpace(res.Error), maxDetailTextLen)}
	}
	out := make([]PushFinding, 0, len(res.Violations))
	for _, v := range res.Violations {
		if len(out) >= maxA11yFindings {
			break
		}
		id := truncateStr(strings.TrimSpace(v.ID), maxRuleIDLen)
		if id == "" {
			// No stable rule id → unusable as a regression key; drop it.
			continue
		}
		impact := truncateStr(strings.TrimSpace(v.Impact), maxImpactLen)
		detail := axeViolation{
			ID:        id,
			Impact:    impact,
			Help:      truncateStr(v.Help, maxHelpTextLen),
			HelpURL:   truncateStr(v.HelpURL, maxURLLen),
			Tags:      boundTags(v.Tags),
			NodeCount: v.NodeCount,
			Nodes:     boundNodes(v.Nodes),
		}
		encoded, err := json.Marshal(detail)
		if err != nil {
			continue
		}
		// Severity carries the SAME bounded impact that goes into the detail (an
		// untruncated v.Impact here would let an oversized value through).
		sev := impact
		if sev == "" {
			// axe impact can be null; auditloop defaults "" to "info" anyway, but be
			// explicit so the pushed severity is never blank.
			sev = severityInfo
		}
		out = append(out, PushFinding{Type: FindingA11y, Severity: sev, Detail: string(encoded)})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// boundTags trims the axe tag list to maxA11yTags AND caps each tag's length (a tag
// is attacker-influenceable via a custom axe config, and capping only the count still
// allowed one arbitrarily long tag).
func boundTags(tags []string) []string {
	out := truncateSlice(tags, maxA11yTags)
	for i, t := range out {
		out[i] = truncateStr(t, maxTagLen)
	}
	return out
}

// boundNodes trims the example-node list and each node's selectors/HTML to the caps.
func boundNodes(nodes []axeNode) []axeNode {
	if len(nodes) == 0 {
		return nil
	}
	if len(nodes) > maxA11yNodes {
		nodes = nodes[:maxA11yNodes]
	}
	out := make([]axeNode, 0, len(nodes))
	for _, n := range nodes {
		targets := truncateSlice(n.Target, maxNodeTargets)
		for i, t := range targets {
			targets[i] = truncateStr(t, maxSelectorLen)
		}
		out = append(out, axeNode{Target: targets, HTML: truncateStr(n.HTML, maxNodeHTMLLen)})
	}
	return out
}

// ConsoleFindings maps FIRST-PARTY console errors to one PushFinding each, carrying
// the actual message text (+ the source URL when known) so the pushed report says
// WHAT the app logged, not merely that it logged something. Third-party console
// errors are skipped by design — they remain in the console_third_party count.
//
// The detail is plain free text: auditloop wraps non-a11y details as
// {"detail": "..."} (json.Marshal escapes it — no injection), and only a11y findings
// need the structured top-level-id shape.
func ConsoleFindings(events []consoleEvent) []PushFinding {
	out := make([]PushFinding, 0, len(events))
	for _, e := range events {
		if !e.FirstPart {
			continue
		}
		if len(out) >= maxConsoleFindings {
			break
		}
		text := truncateStr(strings.TrimSpace(e.Text), maxDetailTextLen)
		if text == "" {
			text = "(empty console error)"
		}
		if u := truncateStr(strings.TrimSpace(e.URL), maxURLLen); u != "" {
			text = fmt.Sprintf("%s (at %s)", text, u)
		}
		out = append(out, PushFinding{Type: FindingConsole, Severity: severitySerious, Detail: text})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NetworkFindings maps FIRST-PARTY network errors to one PushFinding each, carrying
// the HTTP status (or the net:: failure reason) plus the request URL — the shape the
// uploaded *.network.json artifact records ({url,status,reason,first_party}). The
// capture layer records no HTTP method (CDP EventResponseReceived/EventLoadingFailed
// are keyed on the response), so method is deliberately absent rather than guessed.
// Third-party network errors are skipped by design — they remain in the
// network_third_party count.
func NetworkFindings(events []networkEvent) []PushFinding {
	out := make([]PushFinding, 0, len(events))
	for _, e := range events {
		if !e.FirstPart {
			continue
		}
		if len(out) >= maxNetworkFindings {
			break
		}
		u := truncateStr(strings.TrimSpace(e.URL), maxURLLen)
		if u == "" {
			u = "(unknown url)"
		}
		var detail string
		switch {
		case e.Status > 0:
			detail = fmt.Sprintf("HTTP %d %s", e.Status, u)
		case e.Reason != "":
			detail = fmt.Sprintf("%s %s", truncateStr(e.Reason, maxDetailTextLen), u)
		default:
			detail = fmt.Sprintf("request failed %s", u)
		}
		// A 5xx / transport failure is a harder defect than a 4xx (which can be a
		// legitimate not-found probe).
		sev := severitySerious
		if e.Status >= 400 && e.Status < 500 {
			sev = severityModerate
		}
		out = append(out, PushFinding{Type: FindingNetwork, Severity: sev, Detail: detail})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CaptureFindings is the full ordered finding set for one captured view: a11y rules
// first (the gate-critical ones), then first-party console, then first-party network.
// It propagates an axe SCAN FAILURE as an error (see A11yFindings) so a page that was
// never really scanned cannot be pushed as "clean".
func CaptureFindings(vc ViewCapture) ([]PushFinding, error) {
	out, err := A11yFindings(vc.AxeJSON)
	if err != nil {
		return nil, err
	}
	out = append(out, ConsoleFindings(vc.Console)...)
	out = append(out, NetworkFindings(vc.Network)...)
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// truncateStr caps s at n bytes on a UTF-8 rune boundary, so a truncated string is
// never invalid UTF-8 (which json.Marshal would silently replace with U+FFFD).
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	// Back off into the string until the next byte is not a UTF-8 continuation byte.
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// truncateSlice returns at most n elements, as a COPY (callers mutate the result).
func truncateSlice(in []string, n int) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > n {
		in = in[:n]
	}
	return append([]string(nil), in...)
}
