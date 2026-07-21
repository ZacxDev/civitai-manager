module github.com/civitai/civitai-manager

go 1.25.0

require (
	github.com/civitai/cli v0.1.79
	github.com/spf13/cobra v1.8.1
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.34.4
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
	maragu.dev/gomponents v1.3.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

// TEMPORARY: point at the local SDK worktree until civitai/cli PR #172 (which
// promotes pkg/civitai to a public, importable SDK) merges and a tagged release
// is cut. Once that lands, delete this replace directive and pin the tagged
// version in the require block above -- no code change is required, only the
// version selector.
replace github.com/civitai/cli => /home/zach/workspace/civit/cli-pkg-sdk
