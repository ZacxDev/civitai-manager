package comfy

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strconv"
	"time"
)

// maxSeedExclusive bounds a generated seed to [0, 1e15).
//
// It deliberately matches the range the run panel's client-side dice button already
// uses (Math.floor(Math.random() * 1000000000000000), run_params.go) — the seed a
// user rolls by hand and the seed a batch rolls should come from the same space.
// 1e15 < 2^53, so the value round-trips EXACTLY through the JSON number the widget
// slot holds: typedOverrideValue preserves the slot's integer type, and any JS that
// later touches a value above 2^53 would mangle it.
const maxSeedExclusive = 1_000_000_000_000_000

// NewSeed returns a fresh random seed as a decimal string in [0, 1e15).
//
// crypto/rand is used for distribution quality, not for secrecy — a generation seed
// protects nothing. The error branch is unreachable (rand.Int only errors for a
// non-positive bound, and maxSeedExclusive is a positive constant), so it degrades
// to a time-derived value rather than failing a batch over an impossible case.
func NewSeed() string {
	n, err := rand.Int(rand.Reader, big.NewInt(maxSeedExclusive))
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%maxSeedExclusive, 10)
	}
	return n.String()
}

// SeedRunInputs returns the editable SEED inputs of a UI-format graph: exactly the
// DetectRunInputs entries whose Kind is RunInputSeed.
//
// 🔴 THE SELECTOR IS `Kind == RunInputSeed`, AND IT MUST STAY THAT WAY.
// The obvious-looking alternative, isSeedControlSlot, is a DIFFERENT THING and
// using it here is a silent no-op:
//
//   - isSeedControlSlot reports whether a widgets_values slot holds the
//     control_after_generate STRING ("fixed"/"increment"/"decrement"/"randomize").
//     Its only real job is cursor alignment — skipping that extra slot so later
//     widget indices stay correct.
//   - Randomising every slot it matches would write a NUMBER into a STRING slot.
//     ApplyUIWidgetOverrides' typedOverrideValue preserves the slot's JSON type and
//     REFUSES a non-matching replacement, so the write lands nowhere: green tests,
//     dead in production, every batch item byte-identical.
//
// RunInputSeed comes from the curated layout's isSeed/kind pair, and it correctly
// targets the node that HOLDS the value (an upstream rgthree/primitive seed node in
// most real graphs), which is the only place an override reaches ComfyUI from.
//
// TestSeedInputsSelectedByKind pins both halves of this.
func SeedRunInputs(graph json.RawMessage) []RunInput {
	var out []RunInput
	for _, ri := range DetectRunInputs(graph, nil) {
		if ri.Kind == RunInputSeed {
			out = append(out, ri)
		}
	}
	return out
}

// SeedWidgetKeys returns the override keys of a graph's seed inputs, in
// DetectRunInputs order. An empty result means the graph exposes NO editable seed —
// the caller must treat that as "every item of this batch would be identical" and
// say so, never as "no seeds to randomise, carry on".
func SeedWidgetKeys(graph json.RawMessage) []UIWidgetKey {
	inputs := SeedRunInputs(graph)
	if len(inputs) == 0 {
		return nil
	}
	out := make([]UIWidgetKey, 0, len(inputs))
	for _, ri := range inputs {
		out = append(out, UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex})
	}
	return out
}

// ⚠ There is deliberately NO RandomSeedOverrides(graph) helper here. It existed and
// had ZERO non-test callers: production composes the same thing out of the two
// primitives above — internal/web's withFreshSeeds walks the SeedWidgetKeys the
// caller already resolved (from the MODE-APPLIED graph, which a graph-taking helper
// would quietly invite you to skip) and stamps a comfy.NewSeed() on each, copying
// the override map rather than mutating the batch's shared one. Keeping a second
// spelling of that around only offered a way to get it wrong.
