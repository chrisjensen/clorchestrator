package main

import (
	"fmt"
	"os"

	"github.com/chrisjensen/clorchestrate/cmd"
)

const rootUsage = `clorchestrate — orchestrate parallel Claude coding sessions

Usage:
  clorchestrate run <config> <tasks.md>    Iterate a task file; opens one iTerm2 tab per task
  clorchestrate start <config> [...]        Start a single session (see 'clorchestrate start --help')

Run 'clorchestrate <subcommand> --help' for details.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, rootUsage)
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "run":
		err = cmd.RunRun(args, os.Stderr)
	case "start":
		err = cmd.RunStart(args, os.Stderr)
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, rootUsage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", sub)
		fmt.Fprint(os.Stderr, rootUsage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
