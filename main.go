// Command civitai-manager subscribes to CivitAI models and creators and
// auto-queues downloads of new versions, with a local web UI.
package main

import (
	"fmt"
	"os"

	"github.com/civitai/civitai-manager/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
