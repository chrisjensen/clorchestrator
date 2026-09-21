package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/conffile"
	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
	"github.com/chrisjensen/clorchestrate/internal/sync"
	"github.com/chrisjensen/clorchestrate/internal/taskfile"
	"github.com/chrisjensen/clorchestrate/prompts"
	"github.com/chrisjensen/clorchestrate/scripts"
	"github.com/spf13/cobra"
)

type Mode int

const (
	ModeBareSession   Mode = iota + 1 // no handle/branch — plain ssh
	ModeHandleSession                 // handle only — screen session at default dir
	ModeWorktree                      // handle+branch, no issue — worktree + claude
	ModeFullTask                      // handle+branch+issue — full task
)

type openOptions struct {
	issue          string
	extraContext   string
	pkg            string
	openTab        bool
	fresh          bool
	noClaude       bool   // open the screen session in the worktree but don't launch claude
	benchmarkLabel string // non-empty when opening one variant of a benchmark run
	commandLabel   string // non-empty when a task selects a specific [[command]] via 'command:'

	// Hive coordination (multi-session benchmark runs). role is set for hive
	// sessions. Workers run in <runDir>/<benchmarkLabel>, prompted with their
	// task body plus an instruction to use the hive-worker skill; the
	// coordinator runs in runDir itself and is prompted with
	// /hive-coordinate <coordinatorBase>. See the hive-worker / hive-coordinate
	// skills.
	hiveRole        hiveRole
	runDir          string
	coordinatorBase string
}

// hiveRole is a session's role in a hive run; the empty value means not a hive
// session.
type hiveRole string

const (
	hiveRoleWorker      hiveRole = "worker"
	hiveRoleCoordinator hiveRole = "coordinator"
)

func NewOpenCmd() *cobra.Command {
	var opts openOptions
	var benchmark string
	cmd := &cobra.Command{
		Use:   "open <config> [handle] [branch]",
		Short: "launch a Claude session",
		Long: `Launch a session on a remote server or locally.

  clorchestrate open <config>                                               bare ssh session
  clorchestrate open <config> <handle>                                      screen session at default dir
  clorchestrate open <config> <handle> <branch>                             worktree + claude
  clorchestrate open <config> <handle> <branch> --issue <N|url> [flags]    full task

Without --tab the SSH command runs in the current terminal. Tab color (if
DISPATCH_ITERM_TAB_COLOR is set) is applied via escape sequences in both modes.`,
		Args:              cobra.RangeArgs(1, 3),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			var handle, branch string
			if len(args) >= 2 {
				handle = strings.ReplaceAll(args[1], " ", "-")
			}
			if len(args) >= 3 {
				branch = args[2]
			}
			if benchmark != "" {
				// Resolve and validate all labels before any setup work.
				configPath, err := resolveConfigPath(args[0])
				if err != nil {
					return err
				}
				baseCfg, err := config.Parse(configPath)
				if err != nil {
					return err
				}
				resolvedCfg, err := baseCfg.ResolvePackage(opts.pkg)
				if err != nil {
					return err
				}
				var labels []string
				for _, raw := range strings.Split(benchmark, ",") {
					raw = strings.TrimSpace(raw)
					if raw == "" {
						continue
					}
					resolved, err := resolvedCfg.ResolveCommand(raw)
					if err != nil {
						return err
					}
					labels = append(labels, resolved.Label)
				}
				resolveBranch := func(label string) (string, error) {
					if branch == "" {
						return "", nil
					}
					return branch + "-" + label, nil
				}
				return launchLabelSet(configPath, handle, branch, "", resolvedCfg, labels, opts, resolveBranch)
			}
			return openRun(args[0], handle, branch, opts)
		},
	}
	cmd.Flags().StringVar(&opts.issue, "issue", "", "issue ref (number, #NNN, org/repo#NNN, or URL)")
	cmd.Flags().StringVar(&opts.extraContext, "extra-context", "", "extra context for planning prompt")
	cmd.Flags().StringVar(&opts.pkg, "package", "", "package name to resolve repo/setup overrides from config")
	cmd.Flags().BoolVar(&opts.openTab, "tab", false, "open a new iTerm2 tab instead of running in current terminal")
	cmd.Flags().BoolVar(&opts.fresh, "fresh", false, "kill any existing matching screen session before launching")
	cmd.Flags().StringVar(&benchmark, "benchmark", "", "comma-separated command labels; opens one session per label with branch/dir suffixed by label")
	return cmd
}

func openRun(rawConfigPath, handle, branch string, opts openOptions) error {
	configPath, err := resolveConfigPath(rawConfigPath)
	if err != nil {
		return err
	}

	baseCfg, err := config.Parse(configPath)
	if err != nil {
		return err
	}
	cfg, err := baseCfg.ResolvePackage(opts.pkg)
	if err != nil {
		return err
	}

	if cfg.DispatchMode != "" && cfg.DispatchMode != "ssh" {
		return fmt.Errorf("dispatch mode %q not yet implemented (only ssh supported)", cfg.DispatchMode)
	}

	issueNum := ""
	if opts.issue != "" {
		n, err := taskfile.NormalizeIssueArg(opts.issue)
		if err != nil {
			return err
		}
		issueNum = n
	}

	mode, err := detectMode(handle, branch, issueNum)
	if err != nil {
		return err
	}
	// The coordinator has no branch/worktree of its own; run it as a worktree-style
	// session whose working directory is the run dir.
	if opts.hiveRole == hiveRoleCoordinator {
		mode = ModeWorktree
	}

	configID, err := config.ConfigID(configPath)
	if err != nil {
		return err
	}

	sessionName := configID + "_" + handle
	if opts.pkg != "" {
		sessionName = opts.pkg + "_" + handle
	}
	if issueNum != "" {
		sessionName = sessionName + "_" + issueNum
	}
	// runKey identifies this session's run for tab coloring — the same value
	// reconnect.go's runKeyAndLabel recovers later by stripping the
	// _<label>/_coordinator suffix back off the session name, so a run keeps
	// the same color whether just launched or reconnected afterward.
	runKey := sessionName
	if opts.benchmarkLabel != "" {
		sessionName = sessionName + "_" + opts.benchmarkLabel
	}
	if opts.hiveRole == hiveRoleCoordinator {
		sessionName = sessionName + "_coordinator"
	}

	// Temp setup files (task conf, prompt, task.md) are keyed by a per-session
	// slug, not just the handle: hive/benchmark sessions share a handle but need
	// distinct prompt files, since the tab followup cat's the prompt file
	// asynchronously and would otherwise read a sibling session's prompt.
	slug := handle
	if opts.benchmarkLabel != "" {
		slug += "-" + opts.benchmarkLabel
	}
	if opts.hiveRole == hiveRoleCoordinator {
		slug += "-coordinator"
	}

	// worktreeDir is needed both for session detection (--restart sessions are
	// named after the worktree basename) and later for buildRemoteCmd. In hive
	// mode a worker lives in <runDir>/<label> and the coordinator runs in the
	// run dir itself (no worktree of its own).
	worktreeDir := worktreePath(cfg.RemoteRepo, branch, cfg.WorktreePrefix)
	switch opts.hiveRole {
	case hiveRoleWorker:
		worktreeDir = filepath.Join(opts.runDir, opts.benchmarkLabel)
	case hiveRoleCoordinator:
		worktreeDir = opts.runDir
	}
	worktreeBase := filepath.Base(worktreeDir)

	// findSession checks sessionName first, then falls back to worktreeBase so
	// that sessions created by `reconnect --restart` (named after the worktree
	// directory) are detected even when the open-style name doesn't match.
	findSession := func() (id, state string) {
		id, state = findExistingSession(cfg.Server, sessionName)
		if id == "" && worktreeBase != sessionName {
			id, state = findExistingSession(cfg.Server, worktreeBase)
		}
		return
	}

	var existingSessID string
	if mode == ModeFullTask || mode == ModeWorktree {
		if opts.fresh {
			if id, _ := findSession(); id != "" {
				fmt.Fprintf(os.Stderr, "--fresh: killing existing screen session %s (%s)\n", sessionName, id)
				if err := killSession(cfg.Server, id); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: kill failed: %v\n", err)
				}
			}
		}
		id, state := findSession()
		if id != "" && state != "Detached" {
			fmt.Fprintf(os.Stderr, "screen session %s exists but is %s — skipping (use --fresh to take over)\n", sessionName, state)
			return nil
		}
		existingSessID = id
		if existingSessID != "" {
			fmt.Fprintf(os.Stderr, "screen session %s exists and is Detached (%s) — reattaching (re-run with --fresh to start over)\n", sessionName, existingSessID)
		}
	}

	wrotePrompt := false
	if (mode == ModeFullTask || mode == ModeWorktree) && existingSessID == "" {
		location := cfg.Server
		if location == "" {
			location = "local"
		}
		fmt.Fprintf(os.Stderr, "Running setup for %s on %s…\n", handle, location)
		if err := syncServerScripts(cfg.Server); err != nil {
			return err
		}
		tc := conffile.TaskConf{
			Branch:         branch,
			RemoteRepo:     cfg.RemoteRepo,
			WorktreePrefix: cfg.WorktreePrefix,
			PostSetupCmd:   cfg.PostSetupCmd,
		}
		if opts.hiveRole != "" {
			// The checkout script creates the run dir and copies task.md there. A
			// worker gets its worktree at <runDir>/<label>; the coordinator has no
			// worktree (empty WorktreeDir + empty Branch => run-dir-only setup).
			tc.RunDir = opts.runDir
			if opts.hiveRole == hiveRoleWorker {
				tc.WorktreeDir = worktreeDir
			} else {
				tc.Branch = ""
			}
		}
		if mode == ModeFullTask {
			tc.Issue = issueNum
			tc.IssueRepo = cfg.IssueRepo
		}
		if err := writeTaskConf(cfg.Server, slug, tc); err != nil {
			return err
		}
		if !opts.noClaude {
			// The task body (issue ref / description + planning context) is what a
			// session works from. It is always the initial Claude prompt (for a hive
			// worker, with a trailing instruction to use the hive-worker skill); it's
			// also mirrored to <runDir>/task.md in hive mode, since the coordinator
			// reads it too during its merge/review steps.
			var taskBody string
			if mode == ModeFullTask {
				p, err := prompts.Prompt(prompts.PromptData{
					IssueNum:        issueNum,
					IssueRepo:       cfg.IssueRepo,
					IssueURL:        fmt.Sprintf("https://github.com/%s/issues/%s", cfg.IssueRepo, issueNum),
					PlanningContext: cfg.PlanningContext,
					ExtraContext:    opts.extraContext,
				})
				if err != nil {
					return fmt.Errorf("building issue prompt: %w", err)
				}
				taskBody = p
			} else if opts.hiveRole != "" || (mode == ModeWorktree && (opts.extraContext != "" || opts.benchmarkLabel != "")) {
				p, err := prompts.Prompt(prompts.PromptData{
					PlanningContext: cfg.PlanningContext,
					Description:     opts.extraContext,
				})
				if err != nil {
					return fmt.Errorf("building task prompt: %w", err)
				}
				taskBody = p
			}

			if opts.hiveRole != "" {
				// Coordination happens through files in the run dir. A worker's launch
				// prompt is its task body (so the chat history shows what it was asked
				// to do) plus an instruction to work it via the hive-worker skill; the
				// coordinator has no task body, only /hive-coordinate <base>.
				//
				// Only workers write task.md: they run first and (for issue tasks)
				// render the full issue prompt, whereas the coordinator has no issue
				// and would otherwise clobber the run dir's task.md with an empty body.
				// The coordinator still reads task.md itself during its merge/review
				// steps, so it must stay authoritative there regardless of what's in
				// a worker's own chat history.
				if taskBody != "" && opts.hiveRole == hiveRoleWorker {
					if err := writeTaskMD(cfg.Server, slug, taskBody); err != nil {
						return err
					}
				}
				launchPrompt := hiveLaunchPrompt(opts.hiveRole, taskBody, opts.coordinatorBase)
				if err := writePromptFile(cfg.Server, slug, launchPrompt); err != nil {
					return err
				}
				wrotePrompt = true
			} else {
				prompt := taskBody
				if opts.benchmarkLabel != "" && prompt != "" {
					prompt += "\n- Once committed and any quality checks have been completed, touch the file .clorchestrate-done in the working directory.\n" +
						"- .clorchestrate-done MUST NOT be committed — it must remain a local-only file. Add it to .gitignore if necessary.\n"
				}
				if prompt != "" {
					if err := writePromptFile(cfg.Server, slug, prompt); err != nil {
						return err
					}
					wrotePrompt = true
				}
			}
		}
		if err := runSetup(cfg.Server, slug); err != nil {
			return fmt.Errorf("setup failed: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Setup complete — launching session.")
	}

	claudeCmd := cfg.DefaultClaudeCmd()
	if opts.benchmarkLabel != "" {
		labelCmd, err := cfg.CommandByLabel(opts.benchmarkLabel)
		if err != nil {
			return err
		}
		claudeCmd = labelCmd
	} else if opts.commandLabel != "" {
		labelCmd, err := cfg.CommandByLabel(opts.commandLabel)
		if err != nil {
			return err
		}
		claudeCmd = labelCmd
	}

	// Hive sessions launch a skill invocation as the first prompt and must not
	// start in plan mode — the worker writes PLAN.md and later implements, both
	// of which plan mode would block.
	forcePlan := opts.hiveRole == ""
	followup := buildFollowupCmd(mode, slug, worktreeDir, existingSessID, opts.noClaude, wrotePrompt, forcePlan, claudeCmd)

	tabColor := config.ResolveTabColor(cfg.ITermTabColor, configPath)
	if opts.runDir != "" {
		// Sibling label sessions (and the hive coordinator) share runKey —
		// color them by run instead of by config so an A/B run's tabs are
		// visually grouped and distinct from other runs under the same config.
		tabColor = config.RunGroupColor(runKey)
	}
	if opts.openTab {
		// The tab's AppleScript types followup in after the screen session is
		// up, so screen itself only needs to cd when there's no followup to
		// type (noClaude).
		launchCmd := ""
		if opts.noClaude {
			launchCmd = "cd " + worktreeDir
		}
		remoteCmd := buildRemoteCmd(cfg, mode, handle, sessionName, existingSessID, worktreeDir, launchCmd)
		return iterm.OpenTab(iterm.TabOptions{
			TabColorHex: tabColor,
			RemoteCmd:   remoteCmd,
			FollowupCmd: followup,
		})
	}
	// The current terminal has no AppleScript-typed followup step, so the
	// launch command (cd + claude, or just cd for noClaude) must be embedded
	// directly in what screen runs.
	launchCmd := followup
	if launchCmd == "" && opts.noClaude {
		launchCmd = "cd " + worktreeDir
	}
	remoteCmd := buildRemoteCmd(cfg, mode, handle, sessionName, existingSessID, worktreeDir, launchCmd)
	return runInCurrentTerminal(tabColor, remoteCmd)
}

// completeConfigPaths returns bare config names from ~/.clorchestrate/ for the first positional arg.
func completeConfigPaths(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	entries, err := os.ReadDir(filepath.Join(home, ".clorchestrate"))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func syncServerScripts(server string) error {
	all := scripts.All()
	specs := make([]sync.Script, len(all))
	for i, s := range all {
		specs[i] = sync.Script{Name: s.Name, Content: s.Content, Dir: s.Dir}
	}
	return sync.SyncScripts(server, specs, sync.DefaultRunner, func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format, a...)
	})
}
