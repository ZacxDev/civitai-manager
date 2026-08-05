package comfy

import (
	"path"
	"path/filepath"
	"testing"
)

// TestPathBase pins the helper's contract over every separator shape a workflow
// JSON can carry, including the two inputs where it deliberately disagrees with
// path.Base (empty, bare separator, trailing separator).
func TestPathBase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"backslash", `zimage\zit_sda_v1.safetensors`, "zit_sda_v1.safetensors"},
		{"forward slash", "zimage/zit_sda_v1.safetensors", "zit_sda_v1.safetensors"},
		{"no separator", "zit_sda_v1.safetensors", "zit_sda_v1.safetensors"},
		{"mixed separators, backslash last", `a/b\c.safetensors`, "c.safetensors"},
		{"mixed separators, slash last", `a\b/c.safetensors`, "c.safetensors"},
		{"nested backslashes", `models\loras\anime\foo.safetensors`, "foo.safetensors"},
		{"nested slashes", "models/loras/anime/foo.safetensors", "foo.safetensors"},
		{"trailing forward slash", "a/b/", ""},
		{"trailing backslash", `a\b\`, ""},
		{"empty", "", ""},
		{"only a forward slash", "/", ""},
		{"only a backslash", `\`, ""},
		{"leading slash", "/foo.safetensors", "foo.safetensors"},
		{"leading backslash", `\foo.safetensors`, "foo.safetensors"},
		{"windows drive", `C:\models\foo.safetensors`, "foo.safetensors"},
		{"spaces are preserved", `seg a\my model.safetensors`, "my model.safetensors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PathBase(tc.in); got != tc.want {
				t.Fatalf("PathBase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPathBaseIsNotFilepathBaseOnABackslashRef is the POSITIVE CONTROL for the
// whole change: it pins that the stdlib call the converted sites used to make is
// genuinely a no-op here, so a reader can see the defect rather than take it on
// trust. If this ever fails, the host's filepath semantics changed and the bug
// this package fixed would no longer reproduce on this platform.
func TestPathBaseIsNotFilepathBaseOnABackslashRef(t *testing.T) {
	const ref = `zimage\zit_sda_v1.safetensors`

	if filepath.Separator != '/' {
		t.Skipf("this control only holds where filepath.Separator is '/' (got %q)", filepath.Separator)
	}
	if got := filepath.Base(ref); got != ref {
		t.Fatalf("precondition: filepath.Base(%q) = %q, expected the unchanged input on a '/'-separator host", ref, got)
	}
	if got := PathBase(ref); got != "zit_sda_v1.safetensors" {
		t.Fatalf("PathBase(%q) = %q, want the basename", ref, got)
	}
}

// TestPathBaseAgreesWithPathBaseExceptOnTheDocumentedInputs pins the exact,
// bounded delta against path.Base so "it is just path.Base" cannot quietly become
// true or false without this test moving.
func TestPathBaseAgreesWithPathBaseExceptOnTheDocumentedInputs(t *testing.T) {
	// Agreement, on every ordinary shape.
	for _, in := range []string{
		"foo.safetensors",
		"a/foo.safetensors",
		"a/b/foo.safetensors",
		"/foo.safetensors",
	} {
		if got, want := PathBase(in), path.Base(in); got != want {
			t.Fatalf("PathBase(%q) = %q, path.Base = %q; expected agreement", in, got, want)
		}
	}
	// The documented disagreements, all in the fail-closed direction.
	disagree := []struct {
		in       string
		pathBase string
		want     string
	}{
		{"", ".", ""},
		{"/", "/", ""},
		{"a/b/", "b", ""},
	}
	for _, d := range disagree {
		if got := path.Base(d.in); got != d.pathBase {
			t.Fatalf("precondition: path.Base(%q) = %q, want %q", d.in, got, d.pathBase)
		}
		if got := PathBase(d.in); got != d.want {
			t.Fatalf("PathBase(%q) = %q, want %q", d.in, got, d.want)
		}
	}
}
