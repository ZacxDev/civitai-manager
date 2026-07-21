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
	// withinRoots now takes ALREADY-resolved roots (resolved once at the call
	// site); resolveRoots does that resolution.
	roots := resolveRoots([]string{root})

	escaping := filepath.Join(link, "evil.safetensors")
	if withinRoots(escaping, roots) {
		t.Fatalf("symlinked path escaping the root must be rejected: %s", escaping)
	}

	// A normal path genuinely inside the root passes.
	inside := filepath.Join(root, "sub", "ok.safetensors")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, inside, "weights")
	if !withinRoots(inside, roots) {
		t.Fatalf("a normal inside-root path must pass: %s", inside)
	}

	// The root itself is contained.
	if !withinRoots(root, roots) {
		t.Fatal("the root itself must be contained")
	}
}

// TestResolveRootsResolvesSymlinkedRootOnce proves resolveRoots symlink-resolves
// each root up front (#item1): a symlinked root becomes its real target, so
// withinRoots can accept an inside-root path WITHOUT re-resolving the root on
// every call. It also confirms a path under the (symlinked) root is contained
// once the root is pre-resolved.
func TestResolveRootsResolvesSymlinkedRootOnce(t *testing.T) {
	real := t.TempDir()
	parent := t.TempDir()

	// parent/rootlink -> real; the configured scan root is the SYMLINK.
	rootLink := filepath.Join(parent, "rootlink")
	if err := os.Symlink(real, rootLink); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	resolved := resolveRoots([]string{rootLink})
	if len(resolved) != 1 {
		t.Fatalf("resolveRoots returned %d entries, want 1", len(resolved))
	}
	if want := resolveReal(real); resolved[0] != want {
		t.Fatalf("resolveRoots(symlinked root) = %q, want the real target %q", resolved[0], want)
	}

	// A file under the symlinked root is contained when checked against the
	// pre-resolved root.
	inside := filepath.Join(rootLink, "m.safetensors")
	if err := os.WriteFile(inside, []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !withinRoots(inside, resolved) {
		t.Fatalf("path under the symlinked root must be contained: %s", inside)
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
