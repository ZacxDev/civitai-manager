// Package cli wires the cobra command tree for civitai-manager.
package cli

import (
	"github.com/spf13/cobra"
)

// Execute builds and runs the root command.
func Execute() error {
	root := newRootCmd()
	return root.Execute()
}

func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "civitai-manager",
		Short:         "Subscribe to CivitAI models/creators and auto-download new versions",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}
