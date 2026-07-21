module github.com/civitai/civitai-manager

go 1.25.0

require github.com/spf13/cobra v1.8.1

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)

// TEMPORARY: point at the local SDK worktree until civitai/cli PR #172 (which
// promotes pkg/civitai to a public, importable SDK) merges and a tagged release
// is cut. Once that lands, delete this replace directive and pin the tagged
// version in the require block above -- no code change is required, only the
// version selector.
replace github.com/civitai/cli => /home/zach/workspace/civit/cli-pkg-sdk
