package comfyext

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded extension is Python, so the only way to prove its guards actually
// work is to EXECUTE it. These tests do that hermetically: they stub ComfyUI's
// `server` module and a minimal `aiohttp` (the extension touches only
// RouteTableDef + json_response at import time), so no third-party package is
// required — just an interpreter.

const stubAiohttpWeb = `
class RouteTableDef(list):
    def get(self, path):
        def deco(fn):
            self.append(("GET", path, fn))
            return fn
        return deco

    def post(self, path):
        def deco(fn):
            self.append(("POST", path, fn))
            return fn
        return deco


def json_response(payload, status=200):
    return {"status": status, "payload": payload}
`

const stubServer = `
class PromptServer:
    instance = None

    def __init__(self):
        self.routes = None
        self.sent = []

    def send_sync(self, event, data, sid=None):
        self.sent.append((event, data, sid))
`

// pythonDriver loads the installed extension TWICE (two distinct module names,
// exactly as two directories under custom_nodes/ would) and reports what happened,
// then exercises the cross-site guard directly. It prints one JSON object.
const pythonDriver = `
import importlib.util, json, sys

sys.path.insert(0, sys.argv[2])          # stubs dir (aiohttp, server)
from aiohttp import web
import server

server.PromptServer.instance = server.PromptServer()
server.PromptServer.instance.routes = web.RouteTableDef()
routes = server.PromptServer.instance.routes

ext = sys.argv[1]

def load(name):
    spec = importlib.util.spec_from_file_location(name, ext)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)          # must NEVER raise out of the module
    return mod

first = load("cm_copy_one")
routes_after_first = len(routes)
second = load("cm_copy_two")
routes_after_second = len(routes)

class Req:
    def __init__(self, headers):
        self.headers = headers

def rejected(headers):
    return first._reject_forged_request(Req(headers))

JSON = "application/json"
checks = {
    "civitai-manager (no browser headers)": rejected({"Content-Type": JSON}),
    "json with charset":                    rejected({"Content-Type": JSON + "; charset=utf-8"}),
    "same-origin browser":                  rejected({"Content-Type": JSON, "Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8188", "Host": "127.0.0.1:8188"}),
    "simple cross-site text/plain":         rejected({"Content-Type": "text/plain;charset=UTF-8", "Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example", "Host": "127.0.0.1:8188"}),
    "form-urlencoded":                      rejected({"Content-Type": "application/x-www-form-urlencoded"}),
    "multipart":                            rejected({"Content-Type": "multipart/form-data; boundary=x"}),
    "no content type":                      rejected({}),
    "json but cross-site":                  rejected({"Content-Type": JSON, "Sec-Fetch-Site": "cross-site"}),
    "json but foreign origin":              rejected({"Content-Type": JSON, "Origin": "http://evil.example", "Host": "127.0.0.1:8188"}),
}

print(json.dumps({
    "routes_after_first": routes_after_first,
    "routes_after_second": routes_after_second,
    "route_paths": [(m, p) for (m, p, _) in routes],
    "checks": {k: (v if v is None else str(v)) for k, v in checks.items()},
}))
`

// pythonExtension installs the helper into a temp root and returns (python, extPath,
// stubsDir). It skips the test when no interpreter is available.
func pythonExtension(t *testing.T) (string, string, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("no python3 on PATH — cannot execute the embedded ComfyUI extension")
	}

	root := fakeComfyRoot(t)
	if _, err := Install(root); err != nil {
		t.Fatalf("install: %v", err)
	}
	ext := filepath.Join(root, CustomNodesDir, DirName, "__init__.py")

	stubs := t.TempDir()
	mustMkdir(t, filepath.Join(stubs, "aiohttp"))
	mustWrite(t, filepath.Join(stubs, "aiohttp", "__init__.py"), "from . import web\n")
	mustWrite(t, filepath.Join(stubs, "aiohttp", "web.py"), stubAiohttpWeb)
	mustWrite(t, filepath.Join(stubs, "server.py"), stubServer)
	return python, ext, stubs
}

type driverResult struct {
	RoutesAfterFirst  int               `json:"routes_after_first"`
	RoutesAfterSecond int               `json:"routes_after_second"`
	RoutePaths        [][]string        `json:"route_paths"`
	Checks            map[string]string `json:"checks"`
}

func runPythonDriver(t *testing.T) driverResult {
	t.Helper()
	python, ext, stubs := pythonExtension(t)
	driver := filepath.Join(t.TempDir(), "driver.py")
	mustWrite(t, driver, pythonDriver)

	out, err := exec.Command(python, driver, ext, stubs).CombinedOutput()
	if err != nil {
		t.Fatalf("running the extension failed: %v\n%s", err, out)
	}
	line := strings.TrimSpace(string(out))
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = line[i+1:]
	}
	var res driverResult
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		t.Fatalf("driver output is not JSON (%v):\n%s", err, out)
	}
	return res
}

// TestExtensionRefusesDuplicateRegistration is the 🔴 regression pin. ComfyUI
// imports EVERY directory under custom_nodes/ (dot-directories included), so a
// second copy of this package — a .bak folder, a Manager clone, a symlinked root,
// or a staging dir orphaned by a killed install — runs this module twice. Both
// copies appending the same routes makes aiohttp raise inside ComfyUI's
// add_routes, which our try/except CANNOT catch and which aborts startup for the
// WHOLE server. The second copy must therefore register nothing and stay inert.
func TestExtensionRefusesDuplicateRegistration(t *testing.T) {
	res := runPythonDriver(t)

	if res.RoutesAfterFirst != 2 {
		t.Fatalf("first copy registered %d routes, want 2 (ping + open): %v", res.RoutesAfterFirst, res.RoutePaths)
	}
	if res.RoutesAfterSecond != res.RoutesAfterFirst {
		t.Errorf("a SECOND copy registered %d more route(s) — ComfyUI would fail to start entirely: %v",
			res.RoutesAfterSecond-res.RoutesAfterFirst, res.RoutePaths)
	}
	// Sanity: the routes really are ours (so the count above is not vacuous).
	var got []string
	for _, r := range res.RoutePaths {
		if len(r) == 2 {
			got = append(got, r[0]+" "+r[1])
		}
	}
	want := []string{"GET /civitai-manager/ping", "POST /civitai-manager/open"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("registered routes = %v, want %v", got, want)
	}
}

// TestExtensionOpenRejectsCrossSiteRequests pins F3: without a content-type/origin
// check, any page the user browses could fire a SIMPLE cross-origin POST (no
// preflight, no response read, so CORS never blocks it) and make the open editor
// swap graphs — discarding unsaved work.
func TestExtensionOpenRejectsCrossSiteRequests(t *testing.T) {
	res := runPythonDriver(t)

	accepted := []string{
		"civitai-manager (no browser headers)",
		"json with charset",
		"same-origin browser",
	}
	refused := []string{
		"simple cross-site text/plain",
		"form-urlencoded",
		"multipart",
		"no content type",
		"json but cross-site",
		"json but foreign origin",
	}
	for _, name := range accepted {
		reason, ok := res.Checks[name]
		if !ok {
			t.Fatalf("driver did not report %q", name)
		}
		if reason != "" {
			t.Errorf("%s should be ACCEPTED, was refused: %s", name, reason)
		}
	}
	for _, name := range refused {
		reason, ok := res.Checks[name]
		if !ok {
			t.Fatalf("driver did not report %q", name)
		}
		if reason == "" {
			t.Errorf("%s must be REFUSED (it can silently discard the user's unsaved graph)", name)
		}
	}
}
