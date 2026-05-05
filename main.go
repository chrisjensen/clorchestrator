package main

import (
	"fmt"
	"os"

	"github.com/chrisjensen/clorchestrate/cmd"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:          "clorchestrate",
		Short:        "orchestrate parallel Claude coding sessions",
		SilenceUsage: true,
	}
	root.AddCommand(cmd.NewFlockCmd(), cmd.NewOpenCmd(), cmd.NewReconnectCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
