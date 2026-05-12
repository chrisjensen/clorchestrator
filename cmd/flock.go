package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/github"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
	"github.com/chrisjensen/clorchestrate/internal/taskfile"
	"github.com/spf13/cobra"
)

func NewFlockCmd() *cobra.Command {
	var forceBranch, fresh bool
	cmd := &cobra.Command{
		Use:   "flock <config> <tasks.md>",
		Short: "launch all sessions from a task file",
		Long: `Iterate a markdown task file and open one iTerm2 tab per task.

Each '## <handle>' section must reference a GitHub issue (via #NNN,
org/repo#NNN, or a GitHub URL). A 'base: <ref>' line overrides the default
base branch. Runs 'gh issue develop' per task and opens one iTerm2 tab per
task with a pre-configured Claude session.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			return flockRun(args[0], args[1], forceBranch, fresh)
		},
	}
	cmd.Flags().BoolVar(&forceBranch, "force-branch", false, "always create a new branch (fail if one already exists)")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "kill any existing matching screen session before launching")
	return cmd
}

func flockRun(configPath, tasksPath string, forceBranch, fresh bool) error {
	cfg, err := config.Parse(configPath)
	if err != nil {
		return err
	}
	tasks, err := taskfile.Parse(tasksPath)
	if err != nil {
		return err
	}

	configID, err := config.ConfigID(configPath)
	if err != nil {
		return err
	}

	if err := startPackages(cfg, configID, configPath); err != nil {
		return err
	}

	for _, t := range tasks {
		effectiveCfg, err := cfg.ResolvePackage(t.Package)
		if err != nil {
			return fmt.Errorf("task %s: %w", t.Handle, err)
		}

		base := t.BaseBranch
		if base == "" {
			base = effectiveCfg.DefaultBase
		}
		var branch string
		if !forceBranch {
			existing, err := github.ListLinkedBranches(t.IssueNum, effectiveCfg.IssueRepo)
			if err != nil {
				return fmt.Errorf("task %s: list branches: %w", t.Handle, err)
			}
			if len(existing) > 0 {
				branch = existing[0]
				fmt.Fprintf(os.Stderr, "Reusing existing branch for %q (#%s): %s\n", t.Handle, t.IssueNum, branch)
			}
		}

		if branch == "" {
			fmt.Fprintf(os.Stderr, "Creating branch for %q (#%s from %s)...\n", t.Handle, t.IssueNum, base)
			var err error
			branch, err = github.DevelopBranch(github.DevelopArgs{
				IssueNum:         t.IssueNum,
				IssueRepo:        effectiveCfg.IssueRepo,
				BranchRepo:       effectiveCfg.BranchRepo,
				Base:             base,
				BranchNameFormat: effectiveCfg.BranchNameFormat,
				Handle:           t.Handle,
			})
			if err != nil {
				return fmt.Errorf("task %s: %w", t.Handle, err)
			}
			fmt.Fprintf(os.Stderr, "  Branch: %s\n", branch)
		}

		worktreeDir := worktreePath(effectiveCfg.RemoteRepo, branch, effectiveCfg.WorktreePrefix)
		ahead, err := branchAheadCount(effectiveCfg.Server, worktreeDir, base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not check commits ahead for %s: %v — proceeding with planning session\n", branch, err)
			ahead = 0
		}
		if ahead > 0 {
			fmt.Fprintf(os.Stderr, "  Branch is %d commit(s) ahead of %s — opening shell in worktree (no Claude)\n", ahead, base)
		}

		opts := openOptions{
			issue:        t.IssueNum,
			extraContext: t.ExtraContext,
			pkg:          t.Package,
			openTab:      true,
			fresh:        fresh,
			noClaude:     ahead > 0,
		}
		if err := openRun(configPath, t.Handle, branch, opts); err != nil {
			return fmt.Errorf("open for %s: %w", t.Handle, err)
		}
	}
	return nil
}

// aheadShellCmd renders the shell snippet used by branchAheadCount. Paths are
// interpolated unquoted so a leading "~" undergoes tilde expansion on the
// remote shell — bash does not expand "~" inside double-quoted strings.
func aheadShellCmd(worktreeDir, base string) string {
	return fmt.Sprintf(
		`if [ -d %s ]; then git -C %s rev-list --count origin/%s..HEAD; else echo MISSING; fi`,
		worktreeDir, worktreeDir, base,
	)
}

// branchAheadCount returns how many commits the worktree's HEAD is ahead of
// origin/<base>, run on the server (where any unpushed commits live). Returns
// (0, nil) when the worktree dir doesn't exist yet — that's the fresh-setup
// path and there's no work to detect.
func branchAheadCount(server, worktreeDir, base string) (int, error) {
	shellCmd := aheadShellCmd(worktreeDir, base)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, shellCmd).Output()
	}
	if err != nil {
		return 0, fmt.Errorf("rev-list: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "MISSING" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse rev-list output %q: %w", s, err)
	}
	return n, nil
}

// startPackages opens one persistent iTerm tab per package that has a start_cmd,
// unless that package's screen session is already running on the server.
func startPackages(cfg *config.Config, configID, configPath string) error {
	for _, pkg := range cfg.Packages {
		if pkg.StartCmd == "" {
			continue
		}
		sessionName := configID + "_pkg_" + pkg.Name
		id, state := findExistingSession(cfg.Server, sessionName)
		if id != "" {
			fmt.Fprintf(os.Stderr, "Package %q: session %s already %s — skipping\n", pkg.Name, sessionName, state)
			continue
		}
		fmt.Fprintf(os.Stderr, "Starting package %q in session %s\n", pkg.Name, sessionName)

		// Runner color: package_runner_color → package iterm_tab_color → global iterm_tab_color
		runnerColorRaw := pkg.PackageRunnerColor
		if runnerColorRaw == "" {
			runnerColorRaw = pkg.ITermTabColor
		}
		if runnerColorRaw == "" {
			runnerColorRaw = cfg.ITermTabColor
		}
		tabColor := config.ResolveTabColor(runnerColorRaw, configPath)

		var remoteCmd string
		if cfg.Server == "" {
			remoteCmd = fmt.Sprintf("screen -S %s bash -lc 'cd %s && %s'",
				sessionName, pkg.RemoteRepo, pkg.StartCmd)
		} else {
			remoteCmd = fmt.Sprintf(`ssh -t %s "screen -S %s bash -lc 'cd %s && %s'"`,
				cfg.Server, sessionName, pkg.RemoteRepo, pkg.StartCmd)
		}
		if err := iterm.OpenTab(iterm.TabOptions{
			TabColorHex: tabColor,
			RemoteCmd:   remoteCmd,
		}); err != nil {
			return fmt.Errorf("open package tab for %s: %w", pkg.Name, err)
		}
	}
	return nil
}
