//go:build integration

// Package integration holds LIVE tests that exercise the real code paths against
// api.civitai.com. They are gated three ways so ordinary `go test ./...` and CI
// stay green offline:
//
//  1. the `integration` build tag (this file compiles only with -tags integration);
//  2. an env guard — every test skips unless CIVITAI_INTEGRATION=1;
//  3. auth-requiring tests additionally skip when CIVITAI_TOKEN is empty, and the
//     heavyweight real-bytes download test needs CIVITAI_INTEGRATION_DOWNLOAD=1.
//
// Run them with, e.g.:
//
//	CIVITAI_INTEGRATION=1 CIVITAI_TOKEN=xxx \
//	  go test -tags integration ./internal/integration/ -run Integration -v
//
// The tests deliberately reuse the app's OWN helpers (civitai.New, the poller,
// the download worker, hashutil) so they validate the real pipelines, not a
// reimplementation.
package integration

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	sdk "github.com/civitai/cli/pkg/civitai"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// --- Configurable live targets (override via env) ---
//
// These defaults point at long-lived, well-known public CivitAI resources. They
// are best-effort choices made WITHOUT the ability to run the tests live, so if a
// default has since been removed or restructured, override it:
//
//	CIVITAI_TEST_MODEL_ID             a stable public model id (metadata + poller tests)
//	CIVITAI_TEST_DOWNLOAD_VERSION_ID  a SMALL file's model-version id (real-download test)
const (
	// defaultModelID is DreamShaper — one of the most-downloaded, longest-lived
	// public checkpoints on CivitAI. Used only for READ/metadata + a poller seed;
	// its (large) files are never downloaded by the default test set.
	defaultModelID = "4384"

	// defaultDownloadVersionID is a model-version whose PRIMARY file is small, so
	// the guarded real-download test transfers only tens of KB rather than a
	// multi-GB checkpoint. 9208 is the "EasyNegative" textual-inversion embedding
	// (a ~25 KB .pt/.safetensors), a famously long-lived resource. If it has been
	// removed, set CIVITAI_TEST_DOWNLOAD_VERSION_ID to any other small embedding /
	// VAE version id.
	defaultDownloadVersionID = "9208"
)

func modelID() string {
	if v := os.Getenv("CIVITAI_TEST_MODEL_ID"); v != "" {
		return v
	}
	return defaultModelID
}

func downloadVersionID() string {
	if v := os.Getenv("CIVITAI_TEST_DOWNLOAD_VERSION_ID"); v != "" {
		return v
	}
	return defaultDownloadVersionID
}

func baseURL() string {
	if v := os.Getenv("CIVITAI_BASE_URL"); v != "" {
		return v
	}
	return config.DefaultBaseURL
}

// --- Gating helpers ---

// requireIntegration skips unless CIVITAI_INTEGRATION=1.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CIVITAI_INTEGRATION") == "" {
		t.Skip("set CIVITAI_INTEGRATION=1 (and CIVITAI_TOKEN) to run live integration tests")
	}
}

// tokenOrEmpty returns the configured token (may be "").
func tokenOrEmpty() string { return os.Getenv(config.EnvToken) }

// requireToken skips when no token is set, for tests that need authentication.
func requireToken(t *testing.T) string {
	t.Helper()
	tok := tokenOrEmpty()
	if tok == "" {
		t.Skip("set CIVITAI_TOKEN to run this auth-requiring test")
	}
	return tok
}

// newClient builds the app's real SDK-backed client via the app constructor.
func newClient() *sdk.Client {
	return civitai.New(baseURL(), tokenOrEmpty())
}

// bufLogger returns a slog.Logger writing into buf, so tests can assert the token
// never appears in emitted log output.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// assertNoTokenLeak fails if the captured log output contains the secret token.
func assertNoTokenLeak(t *testing.T, logs string, tok string) {
	t.Helper()
	if tok != "" && bytes.Contains([]byte(logs), []byte(tok)) {
		t.Fatal("SECURITY: token leaked into log output")
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// latestVersionID returns the newest model-version id for the default model.
// The API returns modelVersions newest-first.
func latestVersionID(t *testing.T, ctx context.Context, c *sdk.Client) int {
	t.Helper()
	m, _, err := c.GetModel(ctx, modelID())
	if err != nil {
		t.Fatalf("GetModel(%s): %v", modelID(), err)
	}
	if len(m.ModelVersions) == 0 {
		t.Fatalf("model %s has no versions", modelID())
	}
	vid := m.ModelVersions[0].ID
	if vid <= 0 {
		t.Fatalf("model %s latest version has non-positive id %d", modelID(), vid)
	}
	return vid
}

// Test 1: Read/metadata shape.
func TestIntegration_GetModelShape(t *testing.T) {
	requireIntegration(t)
	c := newClient()
	ctx := testContext(t)

	m, raw, err := c.GetModel(ctx, modelID())
	if err != nil {
		t.Fatalf("GetModel(%s): %v", modelID(), err)
	}
	if m.Name == "" {
		t.Error("model Name is empty")
	}
	if len(m.ModelVersions) == 0 {
		t.Fatal("model has no ModelVersions")
	}
	for i, v := range m.ModelVersions {
		if v.ID <= 0 {
			t.Errorf("modelVersions[%d] has non-positive id %d", i, v.ID)
		}
	}
	// Log the (non-secret) shape so a human can eyeball field casing/presence.
	t.Logf("GetModel(%s): id=%d name=%q type=%q versions=%d rawBytes=%d",
		modelID(), m.ID, m.Name, m.Type, len(m.ModelVersions), len(raw))
	t.Logf("  latest version: id=%d name=%q baseModel=%q files=%d",
		m.ModelVersions[0].ID, m.ModelVersions[0].Name,
		m.ModelVersions[0].BaseModel, len(m.ModelVersions[0].Files))
}

// Test 2: Version detail + hashes.
func TestIntegration_VersionDetailHashes(t *testing.T) {
	requireIntegration(t)
	c := newClient()
	ctx := testContext(t)

	vid := latestVersionID(t, ctx, c)
	vd, raw, err := c.GetModelVersion(ctx, strconv.Itoa(vid))
	if err != nil {
		t.Fatalf("GetModelVersion(%d): %v", vid, err)
	}
	if len(vd.Files) == 0 {
		t.Fatalf("version %d has no Files", vid)
	}
	pf := civitai.PrimaryFile(vd.Files)
	if pf == nil {
		t.Fatalf("version %d has no primary file", vid)
	}
	if pf.Hashes.SHA256 == "" {
		t.Errorf("version %d primary file %q has empty SHA256", vid, pf.Name)
	}
	t.Logf("GetModelVersion(%d): name=%q files=%d rawBytes=%d", vid, vd.Name, len(vd.Files), len(raw))
	t.Logf("  primary file: id=%d name=%q type=%q sizeKB=%.1f sha256set=%t",
		pf.ID, pf.Name, pf.Type, pf.SizeKB, pf.Hashes.SHA256 != "")
}

// Test 3: by-hash round-trip — the backbone of the future library-reconcile
// feature. Take the SHA256 from the latest version and resolve it back by hash.
func TestIntegration_ByHashRoundTrip(t *testing.T) {
	requireIntegration(t)
	c := newClient()
	ctx := testContext(t)

	vid := latestVersionID(t, ctx, c)
	vd, _, err := c.GetModelVersion(ctx, strconv.Itoa(vid))
	if err != nil {
		t.Fatalf("GetModelVersion(%d): %v", vid, err)
	}
	pf := civitai.PrimaryFile(vd.Files)
	if pf == nil || pf.Hashes.SHA256 == "" {
		t.Skipf("version %d has no primary file SHA256 to round-trip", vid)
	}

	byHash, _, err := c.GetModelVersionByHash(ctx, pf.Hashes.SHA256)
	if err != nil {
		t.Fatalf("GetModelVersionByHash(%s): %v", pf.Hashes.SHA256, err)
	}
	if byHash.ID != vd.ID {
		t.Errorf("by-hash resolved to version %d, expected %d", byHash.ID, vd.ID)
	}
	t.Logf("by-hash round-trip: sha256=%s -> version %d (expected %d) OK",
		pf.Hashes.SHA256, byHash.ID, vd.ID)
}

// Test 4: Error classification — a nonexistent model id must classify as
// ErrNotFound, and (best-effort) an anonymous auth-required download must not
// succeed with a plain 200.
func TestIntegration_ErrorClassification(t *testing.T) {
	requireIntegration(t)
	ctx := testContext(t)

	t.Run("not_found", func(t *testing.T) {
		c := newClient()
		// A very large, improbable id reliably yields a 404 → ErrNotFound.
		const badID = "999999999"
		_, _, err := c.GetModel(ctx, badID)
		if err == nil {
			t.Fatalf("GetModel(%s) unexpectedly succeeded", badID)
		}
		if !errors.Is(err, civitai.ErrNotFound) {
			t.Fatalf("GetModel(%s): expected ErrNotFound, got %v", badID, err)
		}
		t.Logf("GetModel(%s) correctly classified as ErrNotFound", badID)
	})

	t.Run("unauthorized_download", func(t *testing.T) {
		// Resolve the small file's download URL anonymously (reads need no auth),
		// confirm it is an auth-required (trusted-host) URL, then fetch it WITHOUT a
		// token and assert we do not get the real file (200 OK). This exercises the
		// real auth requirement without leaking anything.
		anon := civitai.New(baseURL(), "")
		vd, _, err := anon.GetModelVersion(ctx, downloadVersionID())
		if err != nil {
			t.Skipf("could not resolve download version %s to probe auth: %v", downloadVersionID(), err)
		}
		pf := civitai.PrimaryFile(vd.Files)
		if pf == nil {
			t.Skip("download version has no primary file to probe")
		}
		dlURL := pf.DownloadURL
		if dlURL == "" {
			dlURL = vd.DownloadURL
		}
		if dlURL == "" || !sdk.DownloadNeedsAuth(dlURL, baseURL()) {
			t.Skipf("download URL %q is not an auth-required (trusted-host) URL; nothing to assert", dlURL)
		}
		resp, err := anon.DownloadFile(ctx, dlURL)
		if err != nil {
			// A transport-level classified error (e.g. ErrUnauthorized) is also a
			// valid "not allowed anonymously" outcome.
			t.Logf("anonymous DownloadFile errored (acceptable): %v", err)
			return
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case 401, 403:
			t.Logf("anonymous auth-required download correctly rejected with HTTP %d", resp.StatusCode)
		case 200:
			t.Fatalf("anonymous download of an auth-required file returned 200 OK (expected 401/403)")
		default:
			// Redirect-to-login or other non-OK: not a clean success. Record it
			// rather than fail, since exact upstream behavior can vary.
			t.Skipf("anonymous auth-required download returned HTTP %d (not a plain 200; behavior recorded)", resp.StatusCode)
		}
	})
}

// Test 5: Poller end-to-end (live read, no download). Drive one real seed poll of
// the actual poller against the default model into a temp SQLite store; assert
// the seen-versions ledger got seeded and nothing was enqueued on the first poll.
func TestIntegration_PollerSeedLive(t *testing.T) {
	requireIntegration(t)
	c := newClient()
	ctx := testContext(t)

	mid, err := civitai.ParseModelRef(modelID())
	if err != nil {
		t.Fatalf("parse model id %q: %v", modelID(), err)
	}

	st, err := store.Open(t.TempDir() + "/poller.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var logs bytes.Buffer
	p := poller.New(st, c, t.TempDir(), bufLogger(&logs))

	// SubscribeModel runs the real verify → create → seed-poll path.
	subID, err := p.SubscribeModel(ctx, mid, poller.SubscribeOptions{NotifyOnly: true})
	if err != nil {
		t.Fatalf("SubscribeModel(%d): %v", mid, err)
	}

	seen, err := st.CountSeen(subID)
	if err != nil {
		t.Fatalf("CountSeen: %v", err)
	}
	if seen == 0 {
		t.Fatal("seed poll did not seed any seen_versions")
	}

	queued, err := st.ListQueue()
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("first (seed) poll enqueued %d download(s); expected 0", len(queued))
	}

	assertNoTokenLeak(t, logs.String(), tokenOrEmpty())
	t.Logf("poller seed: subscription %d seeded %d version(s), enqueued 0 (as expected)", subID, seen)
}
