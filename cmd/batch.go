package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/github"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
	"github.com/chrisjensen/clorchestrate/internal/taskfile"
	"github.com/spf13/cobra"
)

func NewBatchCmd() *cobra.Command {
	var forceBranch, fresh bool
	var benchmark string
	cmd := &cobra.Command{
		Use:     "batch <config> <tasks.md>",
		Aliases: []string{"flock"},
		Short:   "launch all sessions from a task file",
		Long: `Iterate a markdown task file and open one iTerm2 tab per task.

Runs 'gh issue develop' per task and opens one iTerm2 tab per task with a
pre-configured Claude session.

Task file format:

  ## <handle>                    Section heading. The handle names the
                                 branch/session for this task. Required.

  #NNN | org/repo#NNN | URL      Issue reference. One per section,
                                 anywhere in the body. Required — sections
                                 with no issue ref are skipped with a
                                 warning.

  base: <ref>                    Optional. Overrides the default base
                                 branch for this task only.

  package: <name>                Required when the config defines packages.
                                 Selects a [[package]] from the config;
                                 the package's fields override the global
                                 config for this task. Run
                                 'batch <config> --help' to list packages
                                 defined in a given config.

  command: <label>               Optional. Selects a [[command]] from the
                                 config by label (e.g. 'command: kclaude')
                                 to launch this task with instead of the
                                 config's default command. Ignored for
                                 tasks opened via --benchmark, which
                                 already selects a command per label.

  <anything else>                Freeform context appended to the Claude
                                 prompt for this task.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			return batchRun(args[0], args[1], forceBranch, fresh, benchmark)
		},
	}
	cmd.Flags().BoolVar(&forceBranch, "force-branch", false, "always create a new branch (fail if one already exists)")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "kill any existing matching screen session before launching")
	cmd.Flags().StringVar(&benchmark, "benchmark", "", "comma-separated command labels; opens one session per label per task with branch/dir suffixed by label")

	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		defaultHelp(c, args)
		appendPackageHelp(c, args)
	})
	return cmd
}

// appendPackageHelp prints the [[package]] names from the config named in
// args (if any) under the standard help output. Silently no-ops when no
// config arg is present or the config can't be loaded — help should never
// fail.
func appendPackageHelp(c *cobra.Command, args []string) {
	var configArg string
	var cfg *config.Config
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == c.Name() {
			continue
		}
		path, err := resolveConfigPath(a)
		if err != nil {
			continue
		}
		parsed, err := config.Parse(path)
		if err != nil {
			continue
		}
		configArg = a
		cfg = parsed
		break
	}
	if cfg == nil {
		return
	}
	out := c.OutOrStdout()
	fmt.Fprintf(out, "\nPackages defined in %s:\n", configArg)
	if len(cfg.Packages) == 0 {
		fmt.Fprintln(out, "  (none — omit 'package:' from task entries)")
		return
	}
	for _, p := range cfg.Packages {
		fmt.Fprintf(out, "  %s\n", p.Name)
	}
}

func batchRun(configPath, tasksPath string, forceBranch, fresh bool, benchmark string) error {
	configPath, err := resolveConfigPath(configPath)
	if err != nil {
		return err
	}

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

	benchmarkLabels := parseBenchmarkLabels(benchmark)
	// Resolve and validate all benchmark labels against the config before
	// doing any branch creation or session setup.
	for i, raw := range benchmarkLabels {
		resolved, err := cfg.ResolveCommand(raw)
		if err != nil {
			return err
		}
		benchmarkLabels[i] = resolved.Label
	}

	for _, t := range tasks {
		if len(cfg.Packages) > 0 && t.Package == "" {
			fmt.Fprintf(os.Stderr, "error: task %q has no 'package:' but config defines packages — skipping\n", t.Handle)
			continue
		}

		effectiveCfg, err := cfg.ResolvePackage(t.Package)
		if err != nil {
			return fmt.Errorf("task %s: %w", t.Handle, err)
		}

		base := t.BaseBranch
		if base == "" {
			base = effectiveCfg.DefaultBase
		}

		// Determine the label set for this task. --benchmark applies to every task;
		// a comma-separated per-task 'command:' list is the other trigger.
		labels := benchmarkLabels
		if len(labels) == 0 && len(t.Commands) >= 2 {
			for _, raw := range t.Commands {
				resolved, err := effectiveCfg.ResolveCommand(raw)
				if err != nil {
					return fmt.Errorf("task %s: command %q: %w", t.Handle, raw, err)
				}
				labels = append(labels, resolved.Label)
			}
		}
		// Hive coordination (worker worktrees under a shared run dir + a coordinator
		// session) kicks in for any multi-label run.
		hive := len(labels) >= 2

		if len(labels) > 0 {
			// Benchmark mode: one gh-linked branch per label, each suffixed with the label.
			if t.IssueNum != "" {
				closed, err := github.IsIssueClosed(t.IssueNum, effectiveCfg.IssueRepo)
				if err != nil {
					return fmt.Errorf("task %s: check issue state: %w", t.Handle, err)
				}
				if closed {
					fmt.Fprintf(os.Stderr, "Skipping %q (#%s): issue is already closed\n", t.Handle, t.IssueNum)
					continue
				}
			}

			// Compute the base branch name we'd use for the label suffix.
			baseBranchName := benchmarkBaseBranch(effectiveCfg.BranchNameFormat, t.IssueNum, t.Handle)
			// In hive mode the run dir is the per-task worktree path without a label
			// suffix; each worker's worktree is a <runDir>/<label> subdir of it.
			runDir := worktreePath(effectiveCfg.RemoteRepo, baseBranchName, effectiveCfg.WorktreePrefix)

			for _, label := range labels {
				labelBranch := baseBranchName + "-" + label

				if t.IssueNum != "" {
					// Check if this label's branch is already linked to the issue.
					if !forceBranch {
						existing, err := github.ListLinkedBranches(t.IssueNum, effectiveCfg.IssueRepo)
						if err != nil {
							return fmt.Errorf("task %s label %s: list branches: %w", t.Handle, label, err)
						}
						for _, b := range existing {
							if b == labelBranch {
								fmt.Fprintf(os.Stderr, "Reusing existing branch for %q/%s (#%s): %s\n", t.Handle, label, t.IssueNum, labelBranch)
								goto openBenchmarkSession
							}
						}
					}
					fmt.Fprintf(os.Stderr, "Creating branch for %q/%s (#%s from %s)...\n", t.Handle, label, t.IssueNum, base)
					labelBranch, err = github.DevelopBranch(github.DevelopArgs{
						IssueNum:   t.IssueNum,
						IssueRepo:  effectiveCfg.IssueRepo,
						BranchRepo: effectiveCfg.BranchRepo,
						Base:       base,
						Handle:     t.Handle,
						BranchName: labelBranch,
					})
					if err != nil {
						return fmt.Errorf("task %s label %s: %w", t.Handle, label, err)
					}
					fmt.Fprintf(os.Stderr, "  Branch: %s\n", labelBranch)
				} else {
					fmt.Fprintf(os.Stderr, "No-issue task %q/%s — branch: %s\n", t.Handle, label, labelBranch)
				}

			openBenchmarkSession:
				worktreeDir := worktreePath(effectiveCfg.RemoteRepo, labelBranch, effectiveCfg.WorktreePrefix)
				if hive {
					worktreeDir = filepath.Join(runDir, label)
				}
				ahead, err := branchAheadCount(effectiveCfg.Server, worktreeDir, base)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  warning: could not check commits ahead for %s: %v — proceeding with planning session\n", labelBranch, err)
					ahead = 0
				}
				if ahead > 0 {
					fmt.Fprintf(os.Stderr, "  Branch is %d commit(s) ahead of %s — opening shell in worktree (no Claude)\n", ahead, base)
				}

				opts := openOptions{
					issue:          t.IssueNum,
					extraContext:   t.ExtraContext,
					pkg:            t.Package,
					openTab:        true,
					fresh:          fresh,
					noClaude:       ahead > 0,
					benchmarkLabel: label,
				}
				if hive {
					opts.hiveRole = hiveRoleWorker
					opts.runDir = runDir
				}
				if err := openRun(configPath, t.Handle, labelBranch, opts); err != nil {
					return fmt.Errorf("open for %s/%s: %w", t.Handle, label, err)
				}
			}

			// One coordinator session per hive run, launched after the workers so the
			// run dir and task.md already exist. It runs the first label's command.
			if hive {
				coordOpts := openOptions{
					extraContext:    t.ExtraContext,
					pkg:             t.Package,
					openTab:         true,
					fresh:           fresh,
					hiveRole:        hiveRoleCoordinator,
					runDir:          runDir,
					commandLabel:    labels[0],
					coordinatorBase: base,
				}
				if err := openRun(configPath, t.Handle, "", coordOpts); err != nil {
					return fmt.Errorf("open coordinator for %s: %w", t.Handle, err)
				}
			}
			continue
		}

		// Normal (non-benchmark) path.
		var branch string
		if t.IssueNum == "" {
			// No issue — derive branch name from handle.
			branch = t.Handle
			if effectiveCfg.BranchNameFormat != "" && !strings.Contains(effectiveCfg.BranchNameFormat, "{issue}") {
				branch = github.FormatBranchName(effectiveCfg.BranchNameFormat, "", t.Handle)
			}
			fmt.Fprintf(os.Stderr, "No-issue task %q — branch: %s\n", t.Handle, branch)
		} else {
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
				closed, err := github.IsIssueClosed(t.IssueNum, effectiveCfg.IssueRepo)
				if err != nil {
					return fmt.Errorf("task %s: check issue state: %w", t.Handle, err)
				}
				if closed {
					fmt.Fprintf(os.Stderr, "Skipping %q (#%s): issue is already closed\n", t.Handle, t.IssueNum)
					continue
				}
				fmt.Fprintf(os.Stderr, "Creating branch for %q (#%s from %s)...\n", t.Handle, t.IssueNum, base)
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

		// A single 'command:' label picks the launcher; a multi-label list would
		// have been handled by the hive path above and continued past here.
		var commandLabel string
		if len(t.Commands) == 1 {
			resolved, err := effectiveCfg.ResolveCommand(t.Commands[0])
			if err != nil {
				return fmt.Errorf("task %s: command %q: %w", t.Handle, t.Commands[0], err)
			}
			commandLabel = resolved.Label
		}

		opts := openOptions{
			issue:        t.IssueNum,
			extraContext: t.ExtraContext,
			pkg:          t.Package,
			openTab:      true,
			fresh:        fresh,
			noClaude:     ahead > 0,
			commandLabel: commandLabel,
		}
		if err := openRun(configPath, t.Handle, branch, opts); err != nil {
			return fmt.Errorf("open for %s: %w", t.Handle, err)
		}
	}
	return nil
}

// parseBenchmarkLabels splits a comma-separated label string into trimmed,
// non-empty labels. Returns nil when the input is empty.
func parseBenchmarkLabels(s string) []string {
	if s == "" {
		return nil
	}
	var labels []string
	for _, l := range strings.Split(s, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

// benchmarkBaseBranch returns the base branch name used to derive per-label
// benchmark branches. When a branch_name_format is set the formatted name is
// used; otherwise the handle is used as a short, predictable base.
func benchmarkBaseBranch(format, issue, handle string) string {
	if format != "" {
		return github.FormatBranchName(format, issue, handle)
	}
	return handle
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
