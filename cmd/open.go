package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

// runInCurrentTerminal sets the tab color (if any) then runs the remote
// command in the current terminal, inheriting stdin/stdout/stderr.
func runInCurrentTerminal(colorHex, remoteCmd string) error {
	if colorHex != "" {
		iterm.WriteTabColor(os.Stdout, colorHex)
	}
	cmd := exec.Command("sh", "-c", remoteCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func detectMode(handle, branch, issue string) (Mode, error) {
	switch {
	case handle == "" && branch == "" && issue == "":
		return ModeBareSession, nil
	case handle != "" && branch == "" && issue == "":
		return ModeHandleSession, nil
	case handle != "" && branch != "" && issue == "":
		return ModeWorktree, nil
	case handle != "" && branch != "" && issue != "":
		return ModeFullTask, nil
	default:
		return 0, fmt.Errorf("invalid argument combination: handle=%q branch=%q issue=%q", handle, branch, issue)
	}
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

// writeRemoteFile writes content to path on the server (or locally when server
// is ""). desc is used only for error messages.
func writeRemoteFile(server, path, content, desc string) error {
	if server == "" {
		return os.WriteFile(path, []byte(content), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %s on %s: %w", desc, server, err)
	}
	return nil
}

func writeTaskConf(server, handle string, tc conffile.TaskConf) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.conf", handle), conffile.Render(tc), "task conf")
}

func writePromptFile(server, handle, prompt string) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.prompt.md", handle), prompt, "prompt file")
}

// hiveLaunchPrompt builds a hive session's initial Claude prompt. A worker's
// prompt is its task body (so the chat history shows what it was asked to do)
// plus an instruction to work it via the hive-worker skill; the coordinator
// has no task body, only /hive-coordinate <coordinatorBase>.
func hiveLaunchPrompt(role hiveRole, taskBody, coordinatorBase string) string {
	if role == hiveRoleCoordinator {
		return "/hive-coordinate " + coordinatorBase
	}
	prompt := "Use the hive-worker skill to implement this task."
	if taskBody != "" {
		prompt = taskBody + "\n\n" + prompt
	}
	return prompt
}

// writeTaskMD stages the hive task.md body; the checkout script copies it into
// the run dir as task.md (read by the hive-coordinate skill during its
// merge/review steps).
func writeTaskMD(server, handle, body string) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.md", handle), body, "task.md")
}

// runSetup runs ~/bin/worktree-checkout.sh <handle>. When server is set it
// runs over plain (non-tty) SSH; otherwise it runs locally. Output streams
// live to the local terminal so the user sees progress and any failures
// immediately. Returns non-nil error if the script exits non-zero.
func runSetup(server, handle string) error {
	var cmd *exec.Cmd
	if server == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cmd = exec.Command(filepath.Join(home, "bin", "worktree-checkout.sh"), handle)
	} else {
		cmd = exec.Command("ssh", server, fmt.Sprintf("~/bin/worktree-checkout.sh %s", handle))
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolveConfigPath expands a bare name (no path separators) to
// ~/.clorchestrate/{name}.toml. Full and relative paths are returned as-is.
func resolveConfigPath(name string) (string, error) {
	if strings.ContainsRune(name, '/') || strings.HasPrefix(name, ".") {
		return name, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Join(home, ".clorchestrate", name+".toml"), nil
}

// findExistingSession returns the full "PID.name" session ID and its screen
// state (e.g. "Detached", "Attached") for a session matching sessionName.
// When server is "" it queries the local screen; otherwise it queries via SSH.
// Returns "","" if none found or the command fails.
func findExistingSession(server, sessionName string) (id, state string) {
	grepCmd := fmt.Sprintf("screen -ls | grep -F '.%s' | head -1", sessionName)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", grepCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, grepCmd).Output()
	}
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", ""
	}
	sessions := parseScreenLs(string(out))
	if len(sessions) == 0 {
		return "", ""
	}
	s := sessions[0]
	// parseScreenLs strips the PID prefix; rebuild "PID.name" for screen -r/-dr.
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) > 0 {
		id = fields[0]
	} else {
		id = s.name
	}
	return id, s.state
}

// killSession terminates the named screen session, then reaps dead session
// sockets via `screen -wipe`. Runs locally when server is "".
func killSession(server, sessionID string) error {
	shellCmd := fmt.Sprintf("screen -S %s -X quit 2>/dev/null; screen -wipe >/dev/null 2>&1; true", sessionID)
	var cmd *exec.Cmd
	if server == "" {
		cmd = exec.Command("sh", "-c", shellCmd)
	} else {
		cmd = exec.Command("ssh", server, shellCmd)
	}
	return cmd.Run()
}

// screenShellCmd returns the command run inside screen's login shell: either
// a bare login shell, or launchCmd followed by one, so callers that need to
// run something before handing control to the interactive shell (cd into a
// worktree, launch claude) can do so without a separate typed-in followup
// step.
func screenShellCmd(launchCmd string) string {
	if launchCmd == "" {
		return "bash -l"
	}
	return fmt.Sprintf("bash -c '%s && exec bash -l'", launchCmd)
}

// escapeForSSHDoubleQuotes escapes characters in s that would otherwise be
// interpreted by the *local* shell (runInCurrentTerminal execs remoteCmd via
// `sh -c`) when s is embedded inside the outer double-quoted argument of an
// `ssh -t server "..."` command. Without this, a launchCmd containing
// "$(...)" (e.g. the claude prompt's "$(cat file)") would have its command
// substitution run locally instead of on the remote host.
func escapeForSSHDoubleQuotes(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "\\$", "`", "\\`")
	return r.Replace(s)
}

// buildRemoteCmd is the command the iTerm tab (or current terminal) runs
// first: connect to an interactive screen session. No setup happens inside
// screen — that's done by runSetup before the tab opens. When cfg.Server is
// "" the commands run locally without SSH. launchCmd, when non-empty, runs
// inside the screen session before the login shell takes over (e.g. cd into
// worktreeDir, or the full cd+claude followup for the current-terminal path,
// which has no AppleScript-typed-followup step available).
func buildRemoteCmd(cfg *config.Config, mode Mode, handle, sessionName, existingSessID, worktreeDir, launchCmd string) string {
	if cfg.Server == "" {
		switch mode {
		case ModeFullTask, ModeWorktree:
			if existingSessID != "" {
				return fmt.Sprintf("screen -r %s", existingSessID)
			}
			return fmt.Sprintf("screen -S %s %s", sessionName, screenShellCmd(launchCmd))
		case ModeHandleSession:
			return fmt.Sprintf("screen -S %s bash -c 'cd %s && exec bash -l'", sessionName, cfg.RemoteRepo)
		case ModeBareSession:
			return fmt.Sprintf("cd %s && exec bash -l", cfg.RemoteRepo)
		}
		return ""
	}
	switch mode {
	case ModeFullTask, ModeWorktree:
		if existingSessID != "" {
			// Reattach only — caller has already confirmed state=Detached.
			// Using plain -r (not -dr) avoids stealing a session that got
			// reattached between the check and this command running.
			return fmt.Sprintf(`ssh -t %s "screen -r %s"`,
				cfg.Server, existingSessID)
		}
		return fmt.Sprintf(`ssh -t %s "screen -S %s %s"`, cfg.Server, sessionName, screenShellCmd(escapeForSSHDoubleQuotes(launchCmd)))
	case ModeHandleSession:
		return fmt.Sprintf(`ssh -t %s "screen -S %s bash -c 'cd %s && exec bash -l'"`,
			cfg.Server, sessionName, cfg.RemoteRepo)
	case ModeBareSession:
		return fmt.Sprintf(`ssh -t %s "cd %s && exec bash -l"`, cfg.Server, cfg.RemoteRepo)
	}
	return ""
}

// buildFollowupCmd returns the command AppleScript will type into the new tab
// after ssh+screen are established. It cd's into the worktree first so
// claude (and any subsequent commands after claude exits) run from there.
// Returns "" when no followup should be typed (reattach, non-task modes, or
// noClaude — screen has already cd'd into the worktree in that case).
// forcePlan adds --permission-mode plan for the plain (non-template) claude
// launch; hive sessions pass false because the skill drives its own workflow.
func buildFollowupCmd(mode Mode, handle, worktreeDir, existingSessID string, noClaude, withPrompt, forcePlan bool, claudeCmd string) string {
	if existingSessID != "" || noClaude {
		return ""
	}
	switch mode {
	case ModeFullTask, ModeWorktree:
		if withPrompt {
			promptExpr := fmt.Sprintf(`"$(cat /tmp/task-%s.prompt.md)"`, handle)
			if strings.Contains(claudeCmd, "{prompt}") {
				expanded := strings.ReplaceAll(claudeCmd, "{prompt}", promptExpr)
				return fmt.Sprintf("cd %s && %s", worktreeDir, expanded)
			}
			if forcePlan {
				return fmt.Sprintf(`cd %s && %s --permission-mode plan %s`, worktreeDir, claudeCmd, promptExpr)
			}
			return fmt.Sprintf(`cd %s && %s %s`, worktreeDir, claudeCmd, promptExpr)
		}
		return fmt.Sprintf(`cd %s && %s`, worktreeDir, claudeCmd)
	}
	return ""
}

// launchLabelSet opens one session per label: a plain labeled session for a
// single label, or — when there are 2+ labels — hive mode (a shared run dir,
// worker worktrees at <runDir>/<label>, plus a coordinator session). This is
// the one implementation shared by `open --benchmark` and batch's per-task
// `command:`/`--benchmark` handling, so both behave identically for the same
// label count. resolveBranch returns the branch to use for a given label
// (batch creates/reuses it via gh; open's own --benchmark just suffixes the
// user-supplied branch). baseRef, when non-empty, is the branch to check
// "commits ahead" against (batch's resolved default base branch) — passing
// "" skips that check, matching open's own --benchmark behavior.
func launchLabelSet(configPath, handle, baseBranch, baseRef string, cfg *config.Config, labels []string, baseOpts openOptions, resolveBranch func(label string) (string, error)) error {
	hive := len(labels) >= 2
	runDir := worktreePath(cfg.RemoteRepo, baseBranch, cfg.WorktreePrefix)

	for _, label := range labels {
		branch, err := resolveBranch(label)
		if err != nil {
			return err
		}

		worktreeDir := worktreePath(cfg.RemoteRepo, branch, cfg.WorktreePrefix)
		if hive {
			worktreeDir = filepath.Join(runDir, label)
		}

		opts := baseOpts
		opts.benchmarkLabel = label
		if baseRef != "" {
			ahead, err := branchAheadCount(cfg.Server, worktreeDir, baseRef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not check commits ahead for %s: %v — proceeding with planning session\n", branch, err)
				ahead = 0
			}
			if ahead > 0 {
				fmt.Fprintf(os.Stderr, "  Branch is %d commit(s) ahead of %s — opening shell in worktree (no Claude)\n", ahead, baseRef)
			}
			opts.noClaude = ahead > 0
		}
		if hive {
			opts.hiveRole = hiveRoleWorker
			opts.runDir = runDir
		}
		if err := openRun(configPath, handle, branch, opts); err != nil {
			return fmt.Errorf("open for %s/%s: %w", handle, label, err)
		}
	}

	if hive {
		// The coordinator has no issue/branch of its own — clear issue so
		// detectMode doesn't see a dangling issue with no branch.
		coordOpts := baseOpts
		coordOpts.issue = ""
		coordOpts.benchmarkLabel = ""
		coordOpts.hiveRole = hiveRoleCoordinator
		coordOpts.runDir = runDir
		coordOpts.commandLabel = labels[0]
		if err := openRun(configPath, handle, "", coordOpts); err != nil {
			return fmt.Errorf("open coordinator for %s: %w", handle, err)
		}
	}
	return nil
}

// worktreePath mirrors the layout used by scripts/worktree-checkout.sh:
// $(dirname REMOTE_REPO)/<prefix>-<sanitized-branch>. prefix defaults to the
// repo basename when empty. The server will expand ~ when the command runs.
func worktreePath(remoteRepo, branch, prefix string) string {
	repo := strings.TrimRight(remoteRepo, "/")
	parent := "."
	lastSlash := strings.LastIndex(repo, "/")
	if lastSlash > 0 {
		parent = repo[:lastSlash]
	} else if lastSlash == 0 {
		parent = "/"
	}
	if prefix == "" {
		prefix = repo[lastSlash+1:]
	}
	sanitized := strings.ReplaceAll(branch, "/", "-")
	return parent + "/" + prefix + "-" + sanitized
}
