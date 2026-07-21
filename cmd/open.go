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
	"github.com/chrisjensen/clorchestrate/scripts"
	"github.com/spf13/cobra"
)

type Mode int

const (
	ModeBareSession  Mode = iota + 1 // no handle/branch — plain ssh
	ModeHandleSession                // handle only — screen session at default dir
	ModeWorktree                     // handle+branch, no issue — worktree + claude
	ModeFullTask                     // handle+branch+issue — full task
)

type openOptions struct {
	issue          string
	extraContext   string
	pkg            string
	openTab        bool
	fresh          bool
	noClaude       bool   // open the screen session in the worktree but don't launch claude
	benchmarkLabel string // non-empty when opening one variant of a benchmark run
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
				labels := strings.Split(benchmark, ",")
				for _, label := range labels {
					label = strings.TrimSpace(label)
					if label == "" {
						continue
					}
					labelOpts := opts
					labelOpts.benchmarkLabel = label
					labelBranch := branch
					if labelBranch != "" {
						labelBranch = branch + "-" + label
					}
					if err := openRun(args[0], handle, labelBranch, labelOpts); err != nil {
						return err
					}
				}
				return nil
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

	// worktreeDir is needed both for session detection (--restart sessions are
	// named after the worktree basename) and later for buildRemoteCmd.
	worktreeDir := worktreePath(cfg.RemoteRepo, branch, cfg.WorktreePrefix)
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
		if mode == ModeFullTask {
			tc.Issue = issueNum
			tc.IssueRepo = cfg.IssueRepo
		}
		if err := writeTaskConf(cfg.Server, handle, tc); err != nil {
			return err
		}
		if !opts.noClaude {
			var prompt string
			if mode == ModeFullTask {
				prompt = buildPrompt(issueNum, cfg.IssueRepo, cfg.PlanningContext, opts.extraContext)
			} else if mode == ModeWorktree && opts.extraContext != "" {
				prompt = buildTaskPrompt(cfg.PlanningContext, opts.extraContext)
			}
			if prompt != "" {
				if err := writePromptFile(cfg.Server, handle, prompt); err != nil {
					return err
				}
				wrotePrompt = true
			}
		}
		if err := runSetup(cfg.Server, handle); err != nil {
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
	}

	remoteCmd := buildRemoteCmd(cfg, mode, handle, sessionName, existingSessID, worktreeDir, opts.noClaude)
	followup := buildFollowupCmd(mode, handle, worktreeDir, existingSessID, opts.noClaude, wrotePrompt, claudeCmd)

	tabColor := config.ResolveTabColor(cfg.ITermTabColor, configPath)
	if opts.openTab {
		return iterm.OpenTab(iterm.TabOptions{
			TabColorHex: tabColor,
			RemoteCmd:   remoteCmd,
			FollowupCmd: followup,
		})
	}
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
		specs[i] = sync.Script{Name: s.Name, Content: s.Content}
	}
	return sync.SyncScripts(server, specs, sync.DefaultRunner, func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format, a...)
	})
}

func writeTaskConf(server, handle string, tc conffile.TaskConf) error {
	content := conffile.Render(tc)
	path := fmt.Sprintf("/tmp/task-%s.conf", handle)
	if server == "" {
		return os.WriteFile(path, []byte(content), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write task conf on %s: %w", server, err)
	}
	return nil
}

func writePromptFile(server, handle, prompt string) error {
	path := fmt.Sprintf("/tmp/task-%s.prompt.md", handle)
	if server == "" {
		return os.WriteFile(path, []byte(prompt), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write prompt file on %s: %w", server, err)
	}
	return nil
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

// buildTaskPrompt returns a prompt for a task with no GitHub issue.
func buildTaskPrompt(planningContext, description string) string {
	return fmt.Sprintf(`You are working on the following task.

## Repo context
%s

## Task description
%s

## Planning instructions
- Ensure the plan is consistent with the existing architecture and patterns of the codebase
- After generating the plan, check what was missed from the plan
`, planningContext, description)
}

// buildPrompt returns the markdown prompt body (no surrounding quotes) that
// will be written to /tmp/task-<handle>.prompt.md and passed to claude.
func buildPrompt(issueNum, issueRepo, planningContext, extraContext string) string {
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%s", issueRepo, issueNum)
	return fmt.Sprintf(`You are implementing GitHub issue #%s in %s.

## Repo context
%s

## Issue + discussion (must review first)
Open %s and review:
- the issue description
- all comments/discussion (including any linked PRs, decisions, edge cases, and constraints)

## Additional context
%s

## Planning instructions
- Ensure the plan is consistent with the existing architecture and patterns of the codebase
- After generating the plan, check what was missed from the plan
`, issueNum, issueRepo, planningContext, issueURL, extraContext)
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

// buildRemoteCmd is the command the iTerm tab (or current terminal) runs
// first: connect to an interactive screen session. No setup happens inside
// screen — that's done by runSetup before the tab opens. When cfg.Server is
// "" the commands run locally without SSH. When noClaude is true (worktree
// already has commits), screen cd's into worktreeDir directly so the user
// lands at a shell in the right place — no followup is typed.
func buildRemoteCmd(cfg *config.Config, mode Mode, handle, sessionName, existingSessID, worktreeDir string, noClaude bool) string {
	if cfg.Server == "" {
		switch mode {
		case ModeFullTask, ModeWorktree:
			if existingSessID != "" {
				return fmt.Sprintf("screen -r %s", existingSessID)
			}
			if noClaude {
				return fmt.Sprintf("screen -S %s bash -c 'cd %s && exec bash -l'", sessionName, worktreeDir)
			}
			return fmt.Sprintf("screen -S %s bash -l", sessionName)
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
		if noClaude {
			return fmt.Sprintf(`ssh -t %s "screen -S %s bash -c 'cd %s && exec bash -l'"`,
				cfg.Server, sessionName, worktreeDir)
		}
		return fmt.Sprintf(`ssh -t %s "screen -S %s bash -l"`, cfg.Server, sessionName)
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
func buildFollowupCmd(mode Mode, handle, worktreeDir, existingSessID string, noClaude, withPrompt bool, claudeCmd string) string {
	if existingSessID != "" || noClaude {
		return ""
	}
	switch mode {
	case ModeFullTask, ModeWorktree:
		if withPrompt {
			return fmt.Sprintf(`cd %s && %s --permission-mode plan "$(cat /tmp/task-%s.prompt.md)"`, worktreeDir, claudeCmd, handle)
		}
		return fmt.Sprintf(`cd %s && %s`, worktreeDir, claudeCmd)
	}
	return ""
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
