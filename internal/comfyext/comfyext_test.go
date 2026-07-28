package comfyext

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeComfyRoot builds a directory that looks like a ComfyUI install.
func fakeComfyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, CustomNodesDir), 0o755); err != nil {
		t.Fatalf("make custom_nodes: %v", err)
	}
	return root
}

// TestInstallWritesExpectedTree proves an install lands the full extension tree
// (python entrypoint + web script + ownership marker) at
// <root>/custom_nodes/civitai-manager, and that the written files are the
// embedded sources byte-for-byte.
func TestInstallWritesExpectedTree(t *testing.T) {
	root := fakeComfyRoot(t)
	st, err := Install(root)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !st.Installed || st.Foreign {
		t.Fatalf("status after install = %+v, want Installed", st)
	}
	if st.Version != ExtensionVersion || st.Outdated {
		t.Errorf("installed version = %q (outdated=%v), want %q", st.Version, st.Outdated, ExtensionVersion)
	}
	dir := filepath.Join(root, CustomNodesDir, DirName)
	if st.Dir != dir {
		t.Errorf("Dir = %q, want %q", st.Dir, dir)
	}
	for _, rel := range []string{"__init__.py", filepath.Join("web", "civitai_manager.js"), MarkerName} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("expected %s to be installed: %v", rel, err)
		}
	}
	// Every embedded source file must be reproduced verbatim.
	err = fs.WalkDir(extFS, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, embedRoot+"/")
		want, err := fs.ReadFile(extFS, p)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Errorf("installed %s differs from the embedded source", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded tree: %v", err)
	}
	// The python entrypoint must satisfy ComfyUI's custom-node contract.
	py, err := os.ReadFile(filepath.Join(dir, "__init__.py"))
	if err != nil {
		t.Fatalf("read __init__.py: %v", err)
	}
	for _, need := range []string{"NODE_CLASS_MAPPINGS", "NODE_DISPLAY_NAME_MAPPINGS", `WEB_DIRECTORY = "./web"`, "/civitai-manager/ping", "/civitai-manager/open", "send_sync"} {
		if !strings.Contains(string(py), need) {
			t.Errorf("__init__.py missing %q", need)
		}
	}
}

// TestInstallRefusesNonComfyRoot proves a directory without custom_nodes/ (and a
// nonexistent / relative one) is refused and NOTHING is written.
func TestInstallRefusesNonComfyRoot(t *testing.T) {
	bare := t.TempDir()
	if _, err := Install(bare); err == nil {
		t.Fatal("expected an error installing into a directory with no custom_nodes/")
	} else if !strings.Contains(err.Error(), CustomNodesDir) {
		t.Errorf("error should name custom_nodes: %v", err)
	}
	entries, _ := os.ReadDir(bare)
	if len(entries) != 0 {
		t.Errorf("a refused install must write nothing, found %d entries", len(entries))
	}

	if _, err := Install(filepath.Join(bare, "nope")); err == nil {
		t.Error("expected an error for a nonexistent root")
	}
	if _, err := Install("relative/path"); err == nil {
		t.Error("expected an error for a relative root")
	}
	if _, err := Install(""); err == nil {
		t.Error("expected an error for an empty root")
	}

	// A root whose custom_nodes is a FILE, not a directory.
	weird := t.TempDir()
	if err := os.WriteFile(filepath.Join(weird, CustomNodesDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(weird); err == nil {
		t.Error("expected an error when custom_nodes is a file")
	}
}

// TestInstallRefusesForeignDirectory proves an existing civitai-manager directory
// that we did not write is NEVER clobbered — not when it has no marker, not when
// the marker is another tool's, not when it is unparseable, and not when the name
// is taken by a plain file.
func TestInstallRefusesForeignDirectory(t *testing.T) {
	cases := map[string]func(dir string){
		"no marker": func(dir string) {
			mustMkdir(t, dir)
			mustWrite(t, filepath.Join(dir, "someones_work.py"), "print('hi')")
		},
		"other tool's marker": func(dir string) {
			mustMkdir(t, dir)
			mustWrite(t, filepath.Join(dir, MarkerName), `{"tool":"someone-else","extension_version":"9"}`)
		},
		"unparseable marker": func(dir string) {
			mustMkdir(t, dir)
			mustWrite(t, filepath.Join(dir, MarkerName), "not json")
		},
		"name taken by a file": func(dir string) {
			mustWrite(t, dir, "not a directory")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := fakeComfyRoot(t)
			dir := filepath.Join(root, CustomNodesDir, DirName)
			setup(dir)
			before := snapshot(t, dir)

			st := Inspect(root)
			if !st.Foreign || st.Installed {
				t.Fatalf("Inspect = %+v, want Foreign", st)
			}
			if _, err := Install(root); err == nil {
				t.Fatal("expected Install to refuse a foreign directory")
			} else if !strings.Contains(err.Error(), "refusing to overwrite") {
				t.Errorf("error should say it refuses to overwrite: %v", err)
			}
			if got := snapshot(t, dir); got != before {
				t.Errorf("a refused install must leave the directory untouched:\nbefore=%s\nafter =%s", before, got)
			}
			if err := Uninstall(root); err == nil {
				t.Fatal("expected Uninstall to refuse a foreign directory")
			} else if !strings.Contains(err.Error(), "refusing to remove") {
				t.Errorf("error should say it refuses to remove: %v", err)
			}
			if got := snapshot(t, dir); got != before {
				t.Errorf("a refused uninstall must leave the directory untouched")
			}
			// No staging leftovers.
			assertNoStagingLeftovers(t, root)
		})
	}
}

// TestInstallUpdatesOwnPreviousVersion proves an install over OUR OWN older
// install succeeds, refreshes the marker, and clears files the old version left
// behind (the directory is replaced, not merged).
func TestInstallUpdatesOwnPreviousVersion(t *testing.T) {
	root := fakeComfyRoot(t)
	dir := filepath.Join(root, CustomNodesDir, DirName)
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, MarkerName), `{"tool":"civitai-manager","extension_version":"0"}`)
	mustWrite(t, filepath.Join(dir, "stale_from_v0.py"), "# gone after the update")

	st := Inspect(root)
	if !st.Installed || !st.Outdated || st.Version != "0" {
		t.Fatalf("Inspect of a v0 install = %+v, want Installed+Outdated version 0", st)
	}

	st, err := Install(root)
	if err != nil {
		t.Fatalf("update install: %v", err)
	}
	if !st.Installed || st.Outdated || st.Version != ExtensionVersion {
		t.Errorf("status after update = %+v, want current version", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale_from_v0.py")); !os.IsNotExist(err) {
		t.Error("an update must not leave the previous version's files behind")
	}
	if _, err := os.Stat(filepath.Join(dir, "__init__.py")); err != nil {
		t.Errorf("updated install missing __init__.py: %v", err)
	}
	var m Marker
	data, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("marker is not valid json: %v", err)
	}
	if m.Tool != markerTool || m.Version != ExtensionVersion || m.InstalledAt.IsZero() {
		t.Errorf("marker = %+v, want tool/version refreshed with a timestamp", m)
	}
	assertNoStagingLeftovers(t, root)
}

// TestUninstallRemovesOnlyWhatWeWrote proves Uninstall deletes our directory and
// nothing else, and reports ErrNotInstalled when there is nothing to remove.
func TestUninstallRemovesOnlyWhatWeWrote(t *testing.T) {
	root := fakeComfyRoot(t)
	neighbour := filepath.Join(root, CustomNodesDir, "someone-elses-node")
	mustMkdir(t, neighbour)
	mustWrite(t, filepath.Join(neighbour, "__init__.py"), "# not ours")

	if err := Uninstall(root); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("uninstall with nothing installed = %v, want ErrNotInstalled", err)
	}
	if _, err := Install(root); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := Uninstall(root); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, CustomNodesDir, DirName)); !os.IsNotExist(err) {
		t.Error("uninstall must remove the extension directory")
	}
	if _, err := os.Stat(filepath.Join(neighbour, "__init__.py")); err != nil {
		t.Errorf("uninstall must not touch other custom nodes: %v", err)
	}
	if st := Inspect(root); st.Installed || st.Foreign {
		t.Errorf("status after uninstall = %+v, want clean", st)
	}
	if err := Uninstall(""); err == nil {
		t.Error("uninstall with no root should error")
	}
}

// TestSafeJoinContainment proves no path — however hostile — can escape the
// extension directory.
func TestSafeJoinContainment(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "tmp", "ext")
	bad := []string{
		"../escape", "web/../../escape", "..", "../../../../etc/passwd",
		"/etc/passwd", "a/b/../../../c",
	}
	for _, rel := range bad {
		if got, err := safeJoin(dir, rel); err == nil {
			t.Errorf("safeJoin(%q) = %q, want an error", rel, got)
		}
	}
	good := map[string]string{
		"__init__.py":            filepath.Join(dir, "__init__.py"),
		"web/civitai_manager.js": filepath.Join(dir, "web", "civitai_manager.js"),
		".":                      dir,
	}
	for rel, want := range good {
		got, err := safeJoin(dir, rel)
		if err != nil {
			t.Errorf("safeJoin(%q) errored: %v", rel, err)
			continue
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", rel, got, want)
		}
	}
	// relUnder must reject anything outside the embed root.
	if _, err := relUnder("extension", "other/file.py"); err == nil {
		t.Error("relUnder should reject a path outside the embed root")
	}
}

// TestDeriveRootFromModelPath proves the comfy_root default is derived from
// comfy_model_path ONLY when the parent really looks like a ComfyUI install.
func TestDeriveRootFromModelPath(t *testing.T) {
	root := fakeComfyRoot(t)
	models := filepath.Join(root, "models")
	mustMkdir(t, models)
	if got := DeriveRoot(models); got != root {
		t.Errorf("DeriveRoot(%q) = %q, want %q", models, got, root)
	}
	// A models dir whose parent is NOT a ComfyUI install must not be derived from.
	stray := filepath.Join(t.TempDir(), "models")
	mustMkdir(t, stray)
	if got := DeriveRoot(stray); got != "" {
		t.Errorf("DeriveRoot(%q) = %q, want \"\" (parent is not a ComfyUI root)", stray, got)
	}
	if got := DeriveRoot(""); got != "" {
		t.Errorf("DeriveRoot(\"\") = %q, want empty", got)
	}
	if LooksLikeRoot(filepath.Join(root, "nope")) {
		t.Error("LooksLikeRoot must be false for a nonexistent dir")
	}
}

// TestInspectEmptyRoot proves Inspect is safe (and non-committal) for an unset or
// nonsense root — the UI calls it on every render path.
func TestInspectEmptyRoot(t *testing.T) {
	for _, root := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		st := Inspect(root)
		if st.Installed || st.Foreign {
			t.Errorf("Inspect(%q) = %+v, want neither installed nor foreign", root, st)
		}
	}
}

// --- helpers ---

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// snapshot renders a stable description of a path's contents (or its absence) so
// a test can assert "nothing changed".
func snapshot(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return "<absent>"
	}
	if !info.IsDir() {
		data, _ := os.ReadFile(path)
		return "file:" + string(data)
	}
	var b strings.Builder
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		b.WriteString(rel)
		if !d.IsDir() {
			data, _ := os.ReadFile(p)
			b.WriteString("=" + string(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", path, err)
	}
	return b.String()
}

// assertNoStagingLeftovers proves a failed/completed install left no temp dir in
// custom_nodes (ComfyUI would try to import it as a node package).
func assertNoStagingLeftovers(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, CustomNodesDir))
	if err != nil {
		t.Fatalf("read custom_nodes: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".civitai-manager-staging-") || strings.HasPrefix(e.Name(), ".civitai-manager-probe-") {
			t.Errorf("leftover temp entry in custom_nodes: %s", e.Name())
		}
	}
}
