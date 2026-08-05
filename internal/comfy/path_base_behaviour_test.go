package comfy

import (
	"encoding/json"
	"testing"
)

// The one file, spelled three ways. A Windows-authored graph writes the first;
// the other two are the POSITIVE CONTROLS — they were never broken, so if they
// ever fail the fixture is wrong rather than the code.
const (
	refBackslash = `zimage\zit_sda_v1.safetensors`
	refSlash     = "zimage/zit_sda_v1.safetensors"
	refBare      = "zit_sda_v1.safetensors"
	// refAbsent shares no basename with the above and must stay missing on every
	// leg. Without it a test asserting "not missing" is satisfied by a Preflight
	// that never reports anything missing at all.
	refAbsent = `zimage\definitely-not-installed.safetensors`
)

// preflightRefCases is the three-way table both legs run.
var preflightRefCases = []struct {
	name string
	ref  string
}{
	{"backslash (the bug)", refBackslash},
	{"forward slash (positive control)", refSlash},
	{"bare basename (positive control)", refBare},
}

// loaderGraph is one CheckpointLoaderSimple node referencing ref.
func loaderGraph(ref string) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"4": map[string]any{
			"class_type": "CheckpointLoaderSimple",
			"inputs":     map[string]any{"ckpt_name": ref},
		},
	})
	if err != nil {
		panic(err)
	}
	return json.RawMessage(b)
}

// assertIsComboWithChoices is the intermediate-state check for the choices leg:
// it proves the fixture really produced a COMBO carrying the choice under test,
// so a passing case cannot mean "the choices code path never ran". A combo whose
// option list is non-string decodes to IsCombo==true with nil Choices (see
// InputSpec.UnmarshalJSON), which would silently skip every comparison.
func assertIsComboWithChoices(t *testing.T, info ObjectInfo, class, input, wantChoice string) {
	t.Helper()
	sch, ok := info[class]
	if !ok {
		t.Fatalf("fixture: object_info has no %q", class)
	}
	spec, ok := inputSpec(sch, input)
	if !ok {
		t.Fatalf("fixture: %s has no input %q", class, input)
	}
	if !spec.IsCombo {
		t.Fatalf("fixture: %s.%s is not a combo; the choices leg cannot run", class, input)
	}
	if len(spec.Choices) == 0 {
		t.Fatalf("fixture: %s.%s decoded with NO choices; the choices leg cannot run", class, input)
	}
	for _, c := range spec.Choices {
		if c == wantChoice {
			return
		}
	}
	t.Fatalf("fixture: %s.%s choices %v do not contain %q", class, input, spec.Choices, wantChoice)
}

func missingModels(rep PreflightReport) []string { return rep.MissingModels }

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestPreflightFindsAWindowsAuthoredRefViaTheChoicesLeg exercises leg 1: the file
// is known to ComfyUI (it appears in a loader's /object_info combo choices) and is
// NOT in the local library. localHave is pinned false so nothing else can satisfy
// the reference.
func TestPreflightFindsAWindowsAuthoredRefViaTheChoicesLeg(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {
			"input":{"required":{"ckpt_name":[["zimage/zit_sda_v1.safetensors","unrelated.safetensors"],{}]}},
			"input_order":{"required":["ckpt_name"]}
		}
	}`)
	assertIsComboWithChoices(t, info, "CheckpointLoaderSimple", "ckpt_name", refSlash)

	for _, tc := range preflightRefCases {
		t.Run(tc.name, func(t *testing.T) {
			localCalls := 0
			rep := Preflight(loaderGraph(tc.ref), info, func(string) bool {
				localCalls++
				return false
			})
			// The local leg must have been consulted and must have declined, or
			// "not missing" would say nothing about the choices leg.
			if localCalls == 0 {
				t.Fatal("fixture: localHave was never called; the reference never reached the presence checks")
			}
			if got := missingModels(rep); len(got) != 0 {
				t.Fatalf("ref %q reported MISSING via the choices leg: MissingModels=%v", tc.ref, got)
			}
			if !rep.OK {
				t.Fatalf("ref %q: report not OK: %+v", tc.ref, rep)
			}
		})
	}

	// NEGATIVE CONTROL: a reference ComfyUI genuinely does not have is still
	// reported missing, backslash and all.
	rep := Preflight(loaderGraph(refAbsent), info, func(string) bool { return false })
	if !containsStr(missingModels(rep), refAbsent) {
		t.Fatalf("negative control: %q should be MISSING, got MissingModels=%v", refAbsent, missingModels(rep))
	}
}

// TestPreflightFindsAWindowsAuthoredRefViaTheLocalHaveLeg exercises leg 2: the
// file is in the local library and ComfyUI does NOT list it. The local index is
// keyed by BASENAME (store.ResourceBasename), so the fake answers only to the bare
// filename — exactly what makes an un-folded backslash reference miss.
func TestPreflightFindsAWindowsAuthoredRefViaTheLocalHaveLeg(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {
			"input":{"required":{"ckpt_name":[["unrelated.safetensors"],{}]}},
			"input_order":{"required":["ckpt_name"]}
		}
	}`)
	// The choices leg must be UNABLE to satisfy the reference here, or this test
	// would pass through the other leg and prove nothing about localHave.
	assertIsComboWithChoices(t, info, "CheckpointLoaderSimple", "ckpt_name", "unrelated.safetensors")

	for _, tc := range preflightRefCases {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			rep := Preflight(loaderGraph(tc.ref), info, func(name string) bool {
				asked = append(asked, name)
				return name == refBare
			})
			if len(asked) == 0 {
				t.Fatal("fixture: localHave was never called")
			}
			if !containsStr(asked, refBare) {
				t.Fatalf("ref %q: localHave was asked %v, never the basename %q — the library index cannot answer that question",
					tc.ref, asked, refBare)
			}
			if got := missingModels(rep); len(got) != 0 {
				t.Fatalf("ref %q reported MISSING via the localHave leg: MissingModels=%v", tc.ref, got)
			}
		})
	}

	// NEGATIVE CONTROL: the same localHave, a file it does not hold.
	rep := Preflight(loaderGraph(refAbsent), info, func(name string) bool { return name == refBare })
	if !containsStr(missingModels(rep), refAbsent) {
		t.Fatalf("negative control: %q should be MISSING, got MissingModels=%v", refAbsent, missingModels(rep))
	}
}

// TestChoicesToleranceSpansSeparatorSpellings pins the tolerance against the one
// basename collision measured on a live ComfyUI: 232 of 513 combo values carry a
// separator, and "igbaddie-PN.safetensors" exists both bare and under "seg-b/".
// Every spelling must resolve; none may shadow another into a false MISSING.
//
// ⚠ This test was called TestChoicesKeepExactMatchBeforeBasename, which claimed
// something it does not check and CANNOT check: the exact and basename compares are
// ORed per element and both operands are pure, so their written order is a
// short-circuit, not behaviour (swapping them keeps the suite green — measured:
// 545 tests, 0 failures). What is asserted here is the tolerance's EXTENT — which
// spellings are accepted and, just as importantly, which are not.
func TestChoicesToleranceSpansSeparatorSpellings(t *testing.T) {
	choices := []string{"igbaddie-PN.safetensors", "seg-b/igbaddie-PN.safetensors"}

	for _, value := range []string{
		"igbaddie-PN.safetensors",       // exact, first entry
		"seg-b/igbaddie-PN.safetensors", // exact, second entry
		`seg-b\igbaddie-PN.safetensors`, // Windows spelling of the second → basename leg
		"seg-z/igbaddie-PN.safetensors", // a third directory → basename leg
	} {
		if !choicesContainValue(choices, value) {
			t.Errorf("choicesContainValue(%v, %q) = false, want true", choices, value)
		}
	}
	// The tolerance must not become "anything with a familiar-looking name".
	for _, value := range []string{
		"igbaddie-PN2.safetensors",
		`seg-b\other.safetensors`,
		"",
	} {
		if choicesContainValue(choices, value) {
			t.Errorf("choicesContainValue(%v, %q) = true, want false", choices, value)
		}
	}
}

// TestCleanModelQueryFoldsAWindowsSeparator pins the second-order effect: the
// query this builds is sent to civitai.com, and a backslash used to travel with
// it (the API answers 200 with unrelated results, so the failure is silent).
func TestCleanModelQueryFoldsAWindowsSeparator(t *testing.T) {
	// These are MEASURED outputs, not derived ones. Note "moody porn v12 6 00001"
	// keeps its version tokens: versionSuffixRe is anchored at $ and the name ends
	// in "_", so nothing is stripped. That is pre-existing behaviour of the version
	// stripper and is unrelated to the separator fold — pinned here as-is rather
	// than "fixed" in passing.
	cases := []struct {
		in   string
		want string
	}{
		{`ComfyUI\moody-porn-v12.6_00001_.safetensors`, "moody porn v12 6 00001"},
		{"ComfyUI/moody-porn-v12.6_00001_.safetensors", "moody porn v12 6 00001"},
		{"moody-porn-v12.6_00001_.safetensors", "moody porn v12 6 00001"},
		{`seg-a\fabricatedXL_v70.safetensors`, "fabricated XL"},
		{"seg-a/fabricatedXL_v70.safetensors", "fabricated XL"},
		{"fabricatedXL_v70.safetensors", "fabricated XL"},
		{`a\b\c\sd_xl_base_1.0.safetensors`, "sd xl base"},
	}
	for _, tc := range cases {
		got := CleanModelQuery(tc.in)
		if got != tc.want {
			t.Errorf("CleanModelQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// The load-bearing half, independent of the exact wording above: no
		// separator may survive into a query string.
		for _, bad := range []string{`\`, "/"} {
			if containsRune(got, bad) {
				t.Errorf("CleanModelQuery(%q) = %q, which still carries %q", tc.in, got, bad)
			}
		}
	}

	// The relationship the fix is really about: the three spellings of one file
	// must produce ONE query, whatever that query's wording happens to be.
	var seen []string
	for _, tc := range preflightRefCases {
		seen = append(seen, CleanModelQuery(tc.ref))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[0] {
			t.Fatalf("the three spellings of one file produce different queries: %q", seen)
		}
	}
	if seen[0] == "" {
		t.Fatal("fixture: the query is empty, so the agreement above is vacuous")
	}
}

func containsRune(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestResolveModelRefFoldsAWindowsSeparator covers the third consumer: the cloud
// panel's AIR resolution. LocalFileByBasename is keyed by basename, so an
// un-folded reference resolves to nothing and the row renders "unresolved".
func TestResolveModelRefFoldsAWindowsSeparator(t *testing.T) {
	lk := &fakeLookup{
		byBasename: map[string]*LocalMatch{
			refBare: {ModelID: 10, VersionID: 20},
		},
		models: map[[2]int][2]string{
			{10, 20}: {"Checkpoint", "SDXL 1.0"},
		},
	}
	// Fixture precondition: the lookup answers to the basename and ONLY to it.
	if m, _ := lk.LocalFileByBasename(refBare); m == nil {
		t.Fatal("fixture: lookup does not hold the basename")
	}
	if m, _ := lk.LocalFileByBasename(refBackslash); m != nil {
		t.Fatal("fixture: lookup answers to the raw backslash reference, so the fold is untested")
	}

	for _, tc := range preflightRefCases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveModelRef(tc.ref, lk)
			if res.Status != ResolveResolved {
				t.Fatalf("ref %q: Status = %v, want ResolveResolved (URN=%q)", tc.ref, res.Status, res.URN)
			}
			if res.URN == "" {
				t.Fatalf("ref %q: resolved with an empty URN", tc.ref)
			}
			// The Filename is the reference AS WRITTEN — the fold is a lookup key,
			// not a rewrite of what the graph says.
			if res.Filename != tc.ref {
				t.Fatalf("ref %q: Filename = %q, want the reference unchanged", tc.ref, res.Filename)
			}
		})
	}

	// NEGATIVE CONTROL.
	if res := resolveModelRef(refAbsent, lk); res.Status != ResolveUnresolved {
		t.Fatalf("negative control: %q resolved to %v, want ResolveUnresolved", refAbsent, res.Status)
	}
}

// TestModelFileChoicesFoldsAWindowsSeparator covers the consolidated
// choiceBasename: the "◎ ComfyUI has it" index must answer the same way for all
// three spellings of one file.
func TestModelFileChoicesFoldsAWindowsSeparator(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {
			"input":{"required":{"ckpt_name":[["zimage/zit_sda_v1.safetensors"],{}]}},
			"input_order":{"required":["ckpt_name"]}
		}
	}`)
	idx := ModelFileChoices(info)
	if len(idx) != 1 {
		t.Fatalf("fixture: index holds %d entries, want 1: %v", len(idx), idx)
	}
	for _, tc := range preflightRefCases {
		if !HasModelFile(idx, tc.ref) {
			t.Errorf("HasModelFile(%q) = false, want true", tc.ref)
		}
	}
	if HasModelFile(idx, refAbsent) {
		t.Errorf("negative control: HasModelFile(%q) = true, want false", refAbsent)
	}
}

// TestSafeModelDestFoldsAWindowsSeparator covers the download destination: the
// file a Windows-authored reference installs to must be named the same way the
// presence checks look for it, or an install can never satisfy its own reference.
func TestSafeModelDestFoldsAWindowsSeparator(t *testing.T) {
	const root = "/models"
	for _, tc := range preflightRefCases {
		got, err := SafeModelDest(root, "checkpoints", tc.ref)
		if err != nil {
			t.Fatalf("SafeModelDest(%q) error: %v", tc.ref, err)
		}
		if want := "/models/checkpoints/" + refBare; got != want {
			t.Fatalf("SafeModelDest(%q) = %q, want %q", tc.ref, got, want)
		}
	}
	// The containment guards still reject what they always rejected.
	for _, bad := range []string{"", "   ", ".", "..", "/", `\`, `a\..`, "a/.."} {
		if got, err := SafeModelDest(root, "checkpoints", bad); err == nil {
			t.Errorf("SafeModelDest(%q) = %q, want an error", bad, got)
		}
	}
}
