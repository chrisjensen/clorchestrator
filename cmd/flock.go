package cmd

import (
	"fmt"
	"os"

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

	if err := startServices(cfg, configID, configPath); err != nil {
		return err
	}

	for _, t := range tasks {
		effectiveCfg, err := cfg.ResolveService(t.Service)
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

		opts := openOptions{
			issue:       t.IssueNum,
			extraContext: t.ExtraContext,
			service:     t.Service,
			openTab:     true,
			fresh:       fresh,
		}
		if err := openRun(configPath, t.Handle, branch, opts); err != nil {
			return fmt.Errorf("open for %s: %w", t.Handle, err)
		}
	}
	return nil
}

// startServices opens one persistent iTerm tab per service that has a start_cmd,
// unless that service's screen session is already running on the server.
func startServices(cfg *config.Config, configID, configPath string) error {
	for _, svc := range cfg.Services {
		if svc.StartCmd == "" {
			continue
		}
		sessionName := configID + "_svc_" + svc.Name
		id, state := findExistingSession(cfg.Server, sessionName)
		if id != "" {
			fmt.Fprintf(os.Stderr, "Service %q: session %s already %s — skipping\n", svc.Name, sessionName, state)
			continue
		}
		fmt.Fprintf(os.Stderr, "Starting service %q in session %s\n", svc.Name, sessionName)

		// Runner color: service_runner_color → service iterm_tab_color → global iterm_tab_color
		runnerColorRaw := svc.ServiceRunnerColor
		if runnerColorRaw == "" {
			runnerColorRaw = svc.ITermTabColor
		}
		if runnerColorRaw == "" {
			runnerColorRaw = cfg.ITermTabColor
		}
		tabColor := config.ResolveTabColor(runnerColorRaw, configPath)

		var remoteCmd string
		if cfg.Server == "" {
			remoteCmd = fmt.Sprintf("screen -S %s bash -lc 'cd %s && %s'",
				sessionName, svc.RemoteRepo, svc.StartCmd)
		} else {
			remoteCmd = fmt.Sprintf(`ssh -t %s "screen -S %s bash -lc 'cd %s && %s'"`,
				cfg.Server, sessionName, svc.RemoteRepo, svc.StartCmd)
		}
		if err := iterm.OpenTab(iterm.TabOptions{
			TabColorHex: tabColor,
			RemoteCmd:   remoteCmd,
		}); err != nil {
			return fmt.Errorf("open service tab for %s: %w", svc.Name, err)
		}
	}
	return nil
}
