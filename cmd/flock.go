package cmd

import (
	"flag"
	"fmt"
	"io"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/github"
	"github.com/chrisjensen/clorchestrate/internal/taskfile"
)

const flockUsage = `usage:
  clorchestrate flock <config> <tasks.md>

Iterates a markdown task file. Each '## <handle>' section must reference a
GitHub issue (via #NNN, org/repo#NNN, or a GitHub URL). A 'base: <ref>' line
overrides the default base branch. Runs 'gh issue develop' per task and opens
one iTerm2 tab per task with a pre-configured Claude session.
`

func RunFlock(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("flock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, flockUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("expected <config> <tasks.md>")
	}
	configPath := fs.Arg(0)
	tasksPath := fs.Arg(1)

	cfg, err := config.Parse(configPath)
	if err != nil {
		return err
	}
	tasks, err := taskfile.Parse(tasksPath)
	if err != nil {
		return err
	}

	for _, t := range tasks {
		base := t.BaseBranch
		if base == "" {
			base = cfg.DefaultBase
		}
		fmt.Fprintf(stderr, "Creating branch for %q (#%s from %s)...\n", t.Handle, t.IssueNum, base)

		branch, err := github.DevelopBranch(github.DevelopArgs{
			IssueNum:         t.IssueNum,
			IssueRepo:        cfg.IssueRepo,
			BranchRepo:       cfg.BranchRepo,
			Base:             base,
			BranchNameFormat: cfg.BranchNameFormat,
			Handle:           t.Handle,
		})
		if err != nil {
			return fmt.Errorf("task %s: %w", t.Handle, err)
		}
		fmt.Fprintf(stderr, "  Branch: %s\n", branch)

		startArgs := []string{configPath, t.Handle, branch, "--issue", t.IssueNum, "--tab"}
		if t.ExtraContext != "" {
			startArgs = append(startArgs, "--extra-context", t.ExtraContext)
		}
		if err := RunOpen(startArgs, stderr); err != nil {
			return fmt.Errorf("open for %s: %w", t.Handle, err)
		}
	}
	return nil
}
