# civitai-manager

# Overridable live-test targets. Defaults point at long-lived public resources;
# override if a default was removed (see internal/integration/*_test.go).
CIVITAI_TEST_MODEL_ID ?=
CIVITAI_TEST_DOWNLOAD_VERSION_ID ?=

.PHONY: build test vet fmt integration-test integration-test-download

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
