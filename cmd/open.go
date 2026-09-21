package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
	"github.com/chrisjensen/clorchestrate/internal/sync"
	"github.com/chrisjensen/clorchestrate/internal/taskfile"
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

// openSessionPlan is the derived identity of one session launch, shared by
// openRun's phase helpers: the screen session name, the tab-color run key,
// the /tmp/task-<slug>.* file slug, and the worktree directory (plus its
// basename) the session lives in.
type openSessionPlan struct {
	sessionName    string
	runKey         string
	slug           string
	worktreeDir    string
	worktreeBase   string
	existingSessID string // filled in by checkExistingSession
	wrotePrompt    bool   // filled in by runSessionSetup
}

// planSession derives a session's identity: its screen session name, run key,
// temp-file slug, and worktree directory.
func planSession(cfg *config.Config, configID, handle, branch, issueNum string, opts openOptions) openSessionPlan {
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
	return openSessionPlan{
		sessionName:  sessionName,
		runKey:       runKey,
		slug:         slug,
		worktreeDir:  worktreeDir,
		worktreeBase: filepath.Base(worktreeDir),
	}
}

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

// loadOpenConfig resolves the config path, parses it, applies any package
// overrides, rejects unimplemented dispatch modes, and derives the config ID
// used for default session names.
func loadOpenConfig(rawConfigPath, pkg string) (*config.Config, string, string, error) {
	configPath, err := resolveConfigPath(rawConfigPath)
	if err != nil {
		return nil, "", "", err
	}
	baseCfg, err := config.Parse(configPath)
	if err != nil {
		return nil, "", "", err
	}
	cfg, err := baseCfg.ResolvePackage(pkg)
	if err != nil {
		return nil, "", "", err
	}
	if cfg.DispatchMode != "" && cfg.DispatchMode != "ssh" {
		return nil, "", "", fmt.Errorf("dispatch mode %q not yet implemented (only ssh supported)", cfg.DispatchMode)
	}
	configID, err := config.ConfigID(configPath)
	if err != nil {
		return nil, "", "", err
	}
	return cfg, configPath, configID, nil
}

func openRun(rawConfigPath, handle, branch string, opts openOptions) error {
	if err := validateSafeName("handle", handle); err != nil {
		return err
	}
	if err := validateSafeName("branch", branch); err != nil {
		return err
	}

	cfg, configPath, configID, err := loadOpenConfig(rawConfigPath, opts.pkg)
	if err != nil {
		return err
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

	plan := planSession(cfg, configID, handle, branch, issueNum, opts)

	if mode == ModeFullTask || mode == ModeWorktree {
		var skip bool
		plan.existingSessID, skip = checkExistingSession(cfg, opts.fresh, plan.sessionName, plan.worktreeBase)
		if skip {
			return nil
		}
	}

	if (mode == ModeFullTask || mode == ModeWorktree) && plan.existingSessID == "" {
		if err := runSessionSetup(cfg, mode, opts, handle, branch, issueNum, &plan); err != nil {
			return err
		}
	}

	claudeCmd, err := resolveClaudeCmd(cfg, opts)
	if err != nil {
		return err
	}

	return launchSession(cfg, configPath, handle, opts, plan, mode, claudeCmd)
}

// launchSession builds the launch commands and dispatches the session to an
// iTerm tab or the current terminal.
func launchSession(cfg *config.Config, configPath, handle string, opts openOptions, plan openSessionPlan, mode Mode, claudeCmd string) error {
	// Hive sessions launch a skill invocation as the first prompt and must not
	// start in plan mode — the worker writes PLAN.md and later implements, both
	// of which plan mode would block.
	forcePlan := opts.hiveRole == ""
	followup := buildFollowupCmd(mode, plan.slug, plan.worktreeDir, plan.existingSessID, opts.noClaude, plan.wrotePrompt, forcePlan, claudeCmd)

	tabColor := config.ResolveTabColor(cfg.ITermTabColor, configPath)
	if opts.runDir != "" {
		// Sibling label sessions (and the hive coordinator) share runKey —
		// color them by run instead of by config so an A/B run's tabs are
		// visually grouped and distinct from other runs under the same config.
		tabColor = config.RunGroupColor(plan.runKey)
	}
	if opts.openTab {
		// The tab's AppleScript types followup in after the screen session is
		// up, so screen itself only needs to cd when there's no followup to
		// type (noClaude).
		launchCmd := ""
		if opts.noClaude {
			launchCmd = "cd " + plan.worktreeDir
		}
		remoteCmd := buildRemoteCmd(cfg, mode, handle, plan.sessionName, plan.existingSessID, plan.worktreeDir, launchCmd)
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
		launchCmd = "cd " + plan.worktreeDir
	}
	remoteCmd := buildRemoteCmd(cfg, mode, handle, plan.sessionName, plan.existingSessID, plan.worktreeDir, launchCmd)
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
