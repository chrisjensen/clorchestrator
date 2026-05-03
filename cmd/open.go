package cmd

import (
	"bytes"
	"flag"
	"fmt"
	"io"
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
)

type Mode int

const (
	ModeBareSession  Mode = iota + 1 // no handle/branch — plain ssh
	ModeHandleSession                // handle only — screen session at default dir
	ModeWorktree                     // handle+branch, no issue — worktree + claude
	ModeFullTask                     // handle+branch+issue — full task
)

const openUsage = `usage:
  clorchestrate open <config>                                               bare ssh session
  clorchestrate open <config> <handle>                                      screen session at default dir
  clorchestrate open <config> <handle> <branch>                             worktree + claude
  clorchestrate open <config> <handle> <branch> --issue <N|url> [flags]    full task

Flags:
  --issue <ref>          Issue ref: bare number, #NNN, org/repo#NNN, or GitHub URL
  --extra-context <text> Extra context for the planning prompt
  --tab                  Open a new iTerm2 tab instead of running in the current terminal
  --fresh                Kill any existing matching screen session before launching (re-run setup)

Without --tab the SSH command runs in the current terminal. Tab color (if
DISPATCH_ITERM_TAB_COLOR is set) is applied via escape sequences in both modes.
`

func RunOpen(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	issue := fs.String("issue", "", "issue ref (number, #NNN, org/repo#NNN, or URL)")
	extra := fs.String("extra-context", "", "extra context for planning prompt")
	service := fs.String("service", "", "service name to resolve repo/setup overrides from config")
	openTab := fs.Bool("tab", false, "open a new iTerm2 tab instead of running in current terminal")
	fresh := fs.Bool("fresh", false, "kill any existing matching screen session before launching")
	fs.Usage = func() { fmt.Fprint(stderr, openUsage) }

	// Interleaved parser: flags may appear anywhere among positional args.
	// Value flags consume the next token; bool flags stand alone.
	valueFlags := map[string]bool{"--issue": true, "--extra-context": true, "--service": true}
	var positional []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		} else {
			positional = append(positional, a)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) < 1 {
		fs.Usage()
		return fmt.Errorf("missing <config>")
	}
	configPath, err := resolveConfigPath(positional[0])
	if err != nil {
		return err
	}
	var handle, branch string
	if len(positional) >= 2 {
		handle = strings.ReplaceAll(positional[1], " ", "-")
	}
	if len(positional) >= 3 {
		branch = positional[2]
	}
	if len(positional) > 3 {
		fs.Usage()
		return fmt.Errorf("too many positional arguments")
	}

	baseCfg, err := config.Parse(configPath)
	if err != nil {
		return err
	}
	cfg, err := baseCfg.ResolveService(*service)
	if err != nil {
		return err
	}

	if cfg.DispatchMode != "" && cfg.DispatchMode != "ssh" {
		return fmt.Errorf("dispatch mode %q not yet implemented (only ssh supported)", cfg.DispatchMode)
	}

	issueNum := ""
	if *issue != "" {
		n, err := taskfile.NormalizeIssueArg(*issue)
		if err != nil {
			return err
		}
		issueNum = n
	}

	mode, err := detectMode(handle, branch, issueNum)
	if err != nil {
		fs.Usage()
		return err
	}

	configID, err := config.ConfigID(configPath)
	if err != nil {
		return err
	}

	var existingSessID string
	if mode == ModeFullTask || mode == ModeWorktree {
		sessionName := configID + "_" + handle
		if *fresh {
			if id, _ := findExistingSession(cfg.Server, sessionName); id != "" {
				fmt.Fprintf(stderr, "--fresh: killing existing screen session %s (%s)\n", sessionName, id)
				if err := killSession(cfg.Server, id); err != nil {
					fmt.Fprintf(stderr, "  warning: kill failed: %v\n", err)
				}
			}
		}
		id, state := findExistingSession(cfg.Server, sessionName)
		if id != "" && state != "Detached" {
			fmt.Fprintf(stderr, "screen session %s exists but is %s — skipping (use --fresh to take over)\n", sessionName, state)
			return nil
		}
		existingSessID = id
		if existingSessID != "" {
			fmt.Fprintf(stderr, "screen session %s exists and is Detached (%s) — reattaching (re-run with --fresh to start over)\n", sessionName, existingSessID)
		}
	}

	// Setup phase: only when creating a fresh session for a worktree/full-task.
	// Streams output to the local terminal so setup failures surface before
	// any screen session exists.
	if (mode == ModeFullTask || mode == ModeWorktree) && existingSessID == "" {
		location := cfg.Server
		if location == "" {
			location = "local"
		}
		fmt.Fprintf(stderr, "Running setup for %s on %s…\n", handle, location)
		if err := syncServerScripts(cfg.Server, stderr); err != nil {
			return err
		}
		tc := conffile.TaskConf{
			Branch:       branch,
			RemoteRepo:   cfg.RemoteRepo,
			PostSetupCmd: cfg.PostSetupCmd,
		}
		if mode == ModeFullTask {
			tc.Issue = issueNum
			tc.IssueRepo = cfg.IssueRepo
		}
		if err := writeTaskConf(cfg.Server, handle, tc); err != nil {
			return err
		}
		if mode == ModeFullTask {
			prompt := buildPrompt(issueNum, cfg.IssueRepo, cfg.PlanningContext, *extra)
			if err := writePromptFile(cfg.Server, handle, prompt); err != nil {
				return err
			}
		}
		if err := runSetup(cfg.Server, handle); err != nil {
			return fmt.Errorf("setup failed: %w", err)
		}
		fmt.Fprintln(stderr, "Setup complete — launching session.")
	}

	sessionName := configID + "_" + handle
	remoteCmd := buildRemoteCmd(cfg, mode, handle, sessionName, existingSessID)
	worktreeDir := worktreePath(cfg.RemoteRepo, branch)
	followup := buildFollowupCmd(mode, handle, worktreeDir, existingSessID)

	tabColor := config.ResolveTabColor(cfg.ITermTabColor, configPath)
	if *openTab {
		return iterm.OpenTab(iterm.TabOptions{
			TabColorHex: tabColor,
			RemoteCmd:   remoteCmd,
			FollowupCmd: followup,
		})
	}
	return runInCurrentTerminal(tabColor, remoteCmd)
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

func syncServerScripts(server string, stderr io.Writer) error {
	all := scripts.All()
	specs := make([]sync.Script, len(all))
	for i, s := range all {
		specs[i] = sync.Script{Name: s.Name, Content: s.Content}
	}
	return sync.SyncScripts(server, specs, sync.DefaultRunner, func(format string, a ...any) {
		fmt.Fprintf(stderr, format, a...)
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
// "" the commands run locally without SSH.
func buildRemoteCmd(cfg *config.Config, mode Mode, handle, sessionName, existingSessID string) string {
	if cfg.Server == "" {
		switch mode {
		case ModeFullTask, ModeWorktree:
			if existingSessID != "" {
				return fmt.Sprintf("screen -r %s", existingSessID)
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
// Returns "" when no followup should be typed (reattach, or non-task modes).
func buildFollowupCmd(mode Mode, handle, worktreeDir, existingSessID string) string {
	if existingSessID != "" {
		return ""
	}
	switch mode {
	case ModeFullTask:
		return fmt.Sprintf(`cd %s && claude --permission-mode plan "$(cat /tmp/task-%s.prompt.md)"`, worktreeDir, handle)
	case ModeWorktree:
		return fmt.Sprintf(`cd %s && claude`, worktreeDir)
	}
	return ""
}

// worktreePath mirrors the layout used by scripts/worktree-checkout.sh:
// $(dirname REMOTE_REPO)/extractor-<sanitized-branch>. The server will
// expand ~ (tilde) when the command runs, so it's safe to leave raw here.
func worktreePath(remoteRepo, branch string) string {
	repo := strings.TrimRight(remoteRepo, "/")
	parent := "."
	if i := strings.LastIndex(repo, "/"); i > 0 {
		parent = repo[:i]
	} else if i == 0 {
		parent = "/"
	}
	sanitized := strings.ReplaceAll(branch, "/", "-")
	return parent + "/extractor-" + sanitized
}

