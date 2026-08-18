package uxaudit

import (
	_ "embed"
	"encoding/json"
)

// a11yDigestSource is the vendored bounded DOM/accessibility digest script, copied
// VERBATIM from auditloop's crawler (same pattern as axe.min.js / axe.go). It is
// evaluated in the page after axe and returns a JSON string describing the page's
// semantic structure — labels, roles, accessible names, focusability — i.e. the
// AUTHORITATIVE facts a screenshot cannot reveal (an sr-only <label for>, an <a>
// styled as a card).
//
// auditloop feeds this to a DETERMINISTIC, no-LLM gate over the persona evaluator's
// findings. Measured on a real push of this harness, the screenshot-only evaluator
// invented objective a11y false positives that axe on the same tree refutes outright —
// this digest is what closes that.
//
// 🔴 THE GATE HAS TWO STAGES, AND THE SECOND ONE DELETES. Sending a digest is not
// merely granting auditloop the power to REFUTE a wrong claim; as of auditloop main
// @ d19b8a3 (PR #47, "constrain the evaluator to cite only digest-listed selectors")
// it also grants the power to DISCARD a claim this digest simply fails to mention:
//
//	stage 1  groundSelectors  — a MECHANICAL a11y finding (missing label / missing
//	                            accessible name / not keyboard operable) whose selector
//	                            is not in this digest's selector vocabulary is first
//	                            re-anchored via any accessible name it quoted, and
//	                            DROPPED AS UNGROUNDED if that fails.
//	stage 2  dropContradicted — a finding the digest positively REFUTES is dropped.
//
// So a digest that is non-empty but INCOMPLETE for its page deletes TRUE findings. The
// producer rule that follows is: emit a digest that describes the same DOM the
// screenshot and the axe scan describe, or emit none for that page. This harness
// satisfies that structurally — the script below is auditloop's own, byte-identical,
// evaluated on the SAME settled post-prep DOM axe just scanned (capture.go), so its
// element set is exactly as complete as the crawl path's on the same page.
//
// auditloop keeps two safety valves that mean partial-by-CAP is not partial-by-BUG:
// when any list is at its cap, or when the digest carries landmarks but no selectors,
// absence stops being informative and stage 1 does not drop. Neither is something this
// harness may rely on for a digest that is short for any OTHER reason.
//
// MERGED ≠ DEPLOYED: #47 is on auditloop main with CI green; the instance at
// auditloop.zacx.dev may still be running a pre-#47 build, in which case only stage 2
// is live. The producer obligation is the same either way.
//
// It is vendored rather than hand-rolled ON PURPOSE: auditloop's push validator
// rejects the WHOLE multi-page push on any schema violation, and the script already
// enforces every cap (<=40 interactive / <=30 form controls / <=30 landmarks, name
// 120 / text 80 / selector 200) and emits exactly the accepted label_source set.
// Divergence from auditloop's own script is precisely the failure mode the contract
// warns about, so this file must stay byte-identical to its source.
//
//go:embed a11y-digest.js
var a11yDigestSource string

// A11yDigestSource records where a11y-digest.js was copied from, with the checksum
// of the copied bytes. Re-vendor with a plain `cp` from auditloop and bump both.
const A11yDigestSource = "auditloop internal/crawler/a11y-digest.js @ df153b0 (2026-07-29), " +
	"sha256 ce0426e5d43485bd85825c69fee5535bd69047d86198b0483e33926703340312"

// MaxA11yDigestBytes mirrors report.MaxA11yDigestBytes — auditloop REJECTS a digest
// over 256 KiB (and rejecting means 400 for the whole push), so an over-cap digest is
// dropped locally and the page simply carries no digest.
const MaxA11yDigestBytes = 256 << 10

// a11yDigestShape mirrors report.A11yDigest only as far as element COUNTS. The
// elements themselves stay opaque (json.RawMessage): this harness never rewrites the
// digest, it forwards the script's bytes verbatim, so decoding the fields would only
// create an opportunity to drift from the schema.
type a11yDigestShape struct {
	Interactive  []json.RawMessage `json:"interactive"`
	FormControls []json.RawMessage `json:"form_controls"`
	Landmarks    []json.RawMessage `json:"landmarks"`
}

// nonEmptyA11yDigest reports whether raw is a digest worth attaching to a push.
//
// This is the 400-AVOIDANCE GUARD, and it is load-bearing. auditloop REJECTS a digest
// that decodes to `{"interactive":[],"form_controls":[],"landmarks":[]}` — and that
// rejection fails the ENTIRE multi-page push, not just the offending page. An empty
// digest is the normal output of a11y-digest.js on a genuinely bare page AND of its
// catch-all after a JS exception, so it is a case that WILL occur, not a hypothetical.
// The correct producer behaviour is to omit the `a11y_digest` ref for that page.
//
// Unparseable input is likewise treated as empty (omit) rather than forwarded: sending
// bytes auditloop cannot decode would 400 the push for the same all-or-nothing reason.
//
// Every ambiguous case here errs toward NOT attaching, and since auditloop main
// @ d19b8a3 (#47) that direction is doubly right: a missing digest costs only grounding
// precision on one page (auditloop evaluates it screenshot-only, and neither gate stage
// fires), whereas a WRONG or SHORT digest now silently deletes true mechanical a11y
// findings, and a REJECTED digest costs the whole multi-page run. Cheapest failure
// first: omit > partial > malformed.
func nonEmptyA11yDigest(raw []byte) bool {
	if len(raw) == 0 || len(raw) > MaxA11yDigestBytes {
		return false
	}
	var d a11yDigestShape
	if err := json.Unmarshal(raw, &d); err != nil {
		return false
	}
	return len(d.Interactive) > 0 || len(d.FormControls) > 0 || len(d.Landmarks) > 0
}
