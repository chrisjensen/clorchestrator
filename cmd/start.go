package cmd

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	ModeBareSession Mode = iota + 1 // Mode 3: no handle/branch
	ModeWorktree                    // Mode 2: handle+branch, no issue
	ModeFullTask                    // Mode 1: handle+branch+issue
)

const startUsage = `usage:
  clorchestrate start <config>                                               Mode 3: bare ssh session
  clorchestrate start <config> <handle> <branch>                             Mode 2: worktree + claude
  clorchestrate start <config> <handle> <branch> --issue <N|url> [flags]    Mode 1: full task

Flags:
  --issue <ref>          Issue ref: bare number, #NNN, org/repo#NNN, or GitHub URL
  --extra-context <text> Extra context for the planning prompt
  --tab                  Open a new iTerm2 tab instead of running in the current terminal

Without --tab the SSH command runs in the current terminal. Tab color (if
DISPATCH_ITERM_TAB_COLOR is set) is applied via escape sequences in both modes.
`

func RunStart(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	issue := fs.String("issue", "", "issue ref (number, #NNN, org/repo#NNN, or URL)")
	extra := fs.String("extra-context", "", "extra context for planning prompt")
	openTab := fs.Bool("tab", false, "open a new iTerm2 tab instead of running in current terminal")
	fs.Usage = func() { fmt.Fprint(stderr, startUsage) }

	// Separate positional args from flags: positionals only appear before the
	// first flag.
	var positional []string
	var flagArgs []string
	for i, a := range args {
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") {
			flagArgs = args[i:]
			break
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) < 1 {
		fs.Usage()
		return fmt.Errorf("missing <config>")
	}
	configPath := positional[0]
	var handle, branch string
	if len(positional) >= 2 {
		handle = positional[1]
	}
	if len(positional) >= 3 {
		branch = positional[2]
	}
	if len(positional) > 3 {
		fs.Usage()
		return fmt.Errorf("too many positional arguments")
	}

	cfg, err := config.Parse(configPath)
	if err != nil {
		return err
	}

	// TODO: support DISPATCH_MODE=local|mosh in config.
	// Currently only ssh is implemented. Once rewrite is working:
	//   local: run start-task.sh directly (no SSH), open local terminal tab
	//   mosh: replace `ssh -t` with `mosh` in the remote command
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

	if mode != ModeBareSession {
		if cfg.Server == "" {
			return fmt.Errorf("DISPATCH_SERVER is required for Mode 1/2")
		}
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
			tc.PlanningContext = cfg.PlanningContext
			tc.ExtraContext = *extra
		}
		if err := writeTaskConf(cfg.Server, handle, tc); err != nil {
			return err
		}
	}

	remoteCmd := buildRemoteCmd(cfg, mode, handle)

	if *openTab {
		return iterm.OpenTab(iterm.TabOptions{
			TabColorHex: cfg.ITermTabColor,
			RemoteCmd:   remoteCmd,
		})
	}
	return runInCurrentTerminal(cfg.ITermTabColor, remoteCmd)
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
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > /tmp/task-%s.conf", handle))
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write task conf on %s: %w", server, err)
	}
	return nil
}

func buildRemoteCmd(cfg *config.Config, mode Mode, handle string) string {
	switch mode {
	case ModeFullTask, ModeWorktree:
		return fmt.Sprintf(`ssh -t %s "screen -S %s bash -c '~/bin/start-task.sh %s; exec bash'"`,
			cfg.Server, handle, handle)
	case ModeBareSession:
		return fmt.Sprintf(`ssh -t %s "cd %s && exec bash -l"`, cfg.Server, cfg.RemoteRepo)
	}
	return ""
}
