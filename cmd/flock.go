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

Flags:
  --force-branch  Always create a new branch (fail if one already exists)
  --fresh         Kill any existing matching screen session and re-run setup
`

func RunFlock(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("flock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	forceBranch := fs.Bool("force-branch", false, "always create a new branch")
	fresh := fs.Bool("fresh", false, "kill any existing matching screen session before launching")
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
		var branch string
		if !*forceBranch {
			existing, err := github.ListLinkedBranches(t.IssueNum, cfg.IssueRepo)
			if err != nil {
				return fmt.Errorf("task %s: list branches: %w", t.Handle, err)
			}
			if len(existing) > 0 {
				branch = existing[0]
				fmt.Fprintf(stderr, "Reusing existing branch for %q (#%s): %s\n", t.Handle, t.IssueNum, branch)
			}
		}

		if branch == "" {
			fmt.Fprintf(stderr, "Creating branch for %q (#%s from %s)...\n", t.Handle, t.IssueNum, base)
			var err error
			branch, err = github.DevelopBranch(github.DevelopArgs{
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
		}

		startArgs := []string{configPath, t.Handle, branch, "--issue", t.IssueNum, "--tab"}
		if *fresh {
			startArgs = append(startArgs, "--fresh")
		}
		if t.ExtraContext != "" {
			startArgs = append(startArgs, "--extra-context", t.ExtraContext)
		}
		if err := RunOpen(startArgs, stderr); err != nil {
			return fmt.Errorf("open for %s: %w", t.Handle, err)
		}
	}
	return nil
}
