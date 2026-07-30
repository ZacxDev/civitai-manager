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
// auditloop feeds this to a DETERMINISTIC, no-LLM gate that drops persona-evaluator
// findings the DOM positively refutes. Measured on a real push of this harness, the
// screenshot-only evaluator invented objective a11y false positives that axe on the
// same tree refutes outright — this digest is what closes that.
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
// Every ambiguous case here errs toward NOT attaching — a missing digest costs only
// grounding precision on one page, while a rejected push costs the whole run.
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
