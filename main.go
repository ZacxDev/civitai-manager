// Command civitai-manager subscribes to CivitAI models and creators and
// auto-queues downloads of new versions, with a local web UI.
package main

import (
	"fmt"
	"os"

	"github.com/ZacxDev/civitai-manager/internal/cli"
)

// Build metadata, injected at release time via -ldflags:
//
//	-X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}
//
// Defaults are used for `go install` / `go build` and local development.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	bi := cli.ResolveBuildInfo(cli.BuildInfo{Version: version, Commit: commit, Date: date})
	if err := cli.Execute(bi); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
