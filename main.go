package main

import (
	"fmt"
	"os"

	"github.com/chrisjensen/clorchestrate/cmd"
)

const rootUsage = `clorchestrate — orchestrate parallel Claude coding sessions

Usage:
  clorchestrate flock <config> <tasks.md>    Launch all sessions from a task file
  clorchestrate open <config> [...]           Launch a single session (see 'clorchestrate open --help')
  clorchestrate reconnect [<config>]          Reconnect detached/attached screen sessions

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
	case "flock":
		err = cmd.RunFlock(args, os.Stderr)
	case "open":
		err = cmd.RunOpen(args, os.Stderr)
	case "reconnect":
		err = cmd.RunReconnect(args, os.Stderr)
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
