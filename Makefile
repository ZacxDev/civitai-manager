# civitai-manager

# Overridable live-test targets. Defaults point at long-lived public resources;
# override if a default was removed (see internal/integration/*_test.go).
CIVITAI_TEST_MODEL_ID ?=
CIVITAI_TEST_DOWNLOAD_VERSION_ID ?=

.PHONY: build test vet fmt integration-test integration-test-download ux-audit

build:
	go build ./...

# Offline unit tests (integration files are excluded — no -tags integration).
test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

# Live read/metadata + poller integration tests. Requires a token; skips offline.
#   make integration-test CIVITAI_TOKEN=xxx
integration-test:
	CIVITAI_INTEGRATION=1 \
	go test -tags integration ./internal/integration/ -run Integration -v

# The above PLUS the real-bytes authenticated download test.
#   make integration-test-download CIVITAI_TOKEN=xxx
integration-test-download:
	CIVITAI_INTEGRATION=1 CIVITAI_INTEGRATION_DOWNLOAD=1 \
	go test -tags integration ./internal/integration/ -run Integration -v

# Re-runnable auditloop UX-audit walk. Boots the web UI hermetically (temp SQLite +
# deterministic fakes, no live ComfyUI/CivitAI/network), drives headless Chromium
# through the key funnel at mobile+desktop, and writes screenshots + axe/network JSON
# + metadata.json to e2e/uxaudit/artifacts/ (gitignored). chromedp lives ONLY in this
# nested e2e/uxaudit module — it is NOT in the shipped binary's module graph.
#
# Needs a Chromium binary (never downloaded): AUDITLOOP_CHROMIUM=$(command -v chromium)
# or chromium on PATH. Opt-in push to auditloop (non-fatal) when configured:
#   make ux-audit AUDITLOOP_CHROMIUM=$$(command -v chromium) \
#     AUDITLOOP_PUSH_URL=https://auditloop.example \
#     AUDITLOOP_PUSH_TOKENS='{"civitai-manager-funnel":"<token>"}'
ux-audit:
	cd e2e/uxaudit && UXAUDIT_WALK=1 UXAUDIT_OUT=$(CURDIR)/e2e/uxaudit/artifacts \
	go test -run TestUXAuditWalk -count=1 -timeout 15m -v .
