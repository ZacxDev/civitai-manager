package library

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWithinRootsRejectsSymlinkEscape proves the containment guard resolves
// symlinks (#6): a path that LEXICALLY looks inside a scan root but reaches
// OUTSIDE it through a symlinked component is rejected, while a normal path
// genuinely inside the root still passes.
func TestWithinRootsRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling tree, not under root

	// root/link -> outside (a symlink whose target escapes the root).
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	// A leaf under the symlink: lexically root/link/evil.safetensors is "inside"
	// root, but it resolves to outside/evil.safetensors.
	escaping := filepath.Join(link, "evil.safetensors")
	if withinRoots(escaping, []string{root}) {
		t.Fatalf("symlinked path escaping the root must be rejected: %s", escaping)
	}

	// A normal path genuinely inside the root passes.
	inside := filepath.Join(root, "sub", "ok.safetensors")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, inside, "weights")
	if !withinRoots(inside, []string{root}) {
		t.Fatalf("a normal inside-root path must pass: %s", inside)
	}

	// The root itself is contained.
	if !withinRoots(root, []string{root}) {
		t.Fatal("the root itself must be contained")
	}
}

// TestResolveRealNonexistentLeaf proves resolveReal handles a not-yet-existing
// leaf by resolving the nearest existing ancestor and rejoining the leaf — so a
// not-yet-created quarantine destination is still judged against its real parent.
func TestResolveRealNonexistentLeaf(t *testing.T) {
	realDir := t.TempDir()
	target := t.TempDir()

	// realDir/link -> target; the leaf "new.bin" does not exist yet.
	link := filepath.Join(realDir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	got := resolveReal(filepath.Join(link, "new.bin"))
	// Resolve target too (t.TempDir may itself contain symlink components, e.g.
	// macOS /var -> /private/var), so we compare like-for-like.
	want := filepath.Join(resolveReal(target), "new.bin")
	if got != want {
		t.Fatalf("resolveReal(nonexistent leaf under symlink) = %q, want %q", got, want)
	}
}
