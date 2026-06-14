package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
	"github.com/spf13/cobra"
)

type screenSession struct {
	name  string
	state string
}

type configEntry struct {
	path string
	cfg  *config.Config
}

func NewReconnectCmd() *cobra.Command {
	var force bool
	var restart bool
	cmd := &cobra.Command{
		Use:   "reconnect [config]",
		Short: "reconnect detached screen sessions",
		Long: `Reconnect detached or attached screen sessions in new iTerm2 tabs.

Without <config>: reconnect all sessions for every config in ~/.clorchestrate/.
With <config>: reconnect only sessions belonging to that config.

By default only Detached sessions are reattached (via 'screen -r'), so
sessions already open in another tab are left alone. Pass --force to
forcibly reattach any matching session (via 'screen -dr'), detaching any
currently-connected client.

Pass --restart to enumerate worktree directories derived from each config and
start a new screen session (running 'claude --continue') for any that does not
already have one. Session names are the worktree directory basename.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			if restart {
				return reconnectRestart(args)
			}
			return reconnectRun(args, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "forcibly reattach matching sessions using 'screen -dr'")
	cmd.Flags().BoolVar(&restart, "restart", false, "start new sessions for worktree dirs that have no screen session")
	return cmd
}

func reconnectRun(args []string, force bool) error {
	var entries []configEntry
	if len(args) == 1 {
		configPath, err := resolveConfigPath(args[0])
		if err != nil {
			return err
		}
		cfg, err := config.Parse(configPath)
		if err != nil {
			return err
		}
		entries = []configEntry{{path: configPath, cfg: cfg}}
	} else {
		var err error
		entries, err = loadAllConfigs()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(os.Stderr, "no configs found in ~/.clorchestrate/")
			return nil
		}
	}

	// Group by server to SSH once per unique server. Empty server key = local.
	byServer := map[string][]configEntry{}
	for _, e := range entries {
		byServer[e.cfg.Server] = append(byServer[e.cfg.Server], e)
	}

	for server, serverEntries := range byServer {
		sessions, err := listScreenSessions(server)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not list sessions on %s: %v\n", server, err)
			continue
		}

		for _, e := range serverEntries {
			configID, err := config.ConfigID(e.path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not determine config ID for %s: %v\n", e.path, err)
				continue
			}

			type matcher struct {
				prefix   string
				tabColor string
			}
			matchers := []matcher{
				{prefix: configID + "_", tabColor: config.ResolveTabColor(e.cfg.ITermTabColor, e.path)},
			}
			for _, pkg := range e.cfg.Packages {
				if pkg.Name == "" {
					continue
				}
				pkgColor := pkg.ITermTabColor
				if pkgColor == "" {
					pkgColor = e.cfg.ITermTabColor
				}
				matchers = append(matchers, matcher{
					prefix:   pkg.Name + "_",
					tabColor: config.ResolveTabColor(pkgColor, e.path),
				})
			}

			screenAction := "screen -r"
			if force {
				screenAction = "screen -dr"
			}
			for _, sess := range sessions {
				var matched *matcher
				for i := range matchers {
					if strings.HasPrefix(sess.name, matchers[i].prefix) {
						matched = &matchers[i]
						break
					}
				}
				if matched == nil {
					continue
				}
				if !force && sess.state != "Detached" {
					fmt.Fprintf(os.Stderr, "skipping %s (%s) — pass --force to reattach\n", sess.name, sess.state)
					continue
				}
				var reconnectCmd string
				if server == "" {
					reconnectCmd = fmt.Sprintf("%s '%s'", screenAction, sess.name)
				} else {
					reconnectCmd = fmt.Sprintf(`ssh -t %s "%s '%s'"`, server, screenAction, sess.name)
				}
				if err := iterm.OpenTab(iterm.TabOptions{
					TabColorHex: matched.tabColor,
					RemoteCmd:   reconnectCmd,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not open tab for session %s: %v\n", sess.name, err)
				}
			}
		}
	}

	return nil
}

func loadAllConfigs() ([]configEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".clorchestrate")
	dirEntries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config dir: %w", err)
	}

	var result []configEntry
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cfg, err := config.Parse(path)
		if err != nil {
			continue
		}
		result = append(result, configEntry{path: path, cfg: cfg})
	}
	return result, nil
}

func listScreenSessions(server string) ([]screenSession, error) {
	// screen -ls exits non-zero even when sessions exist; ignore exit code.
	var out []byte
	if server == "" {
		out, _ = exec.Command("screen", "-ls").CombinedOutput()
	} else {
		out, _ = exec.Command("ssh", server, "screen -ls").CombinedOutput()
	}
	return parseScreenLs(string(out)), nil
}

// parseScreenLs parses the output of `screen -ls` into a list of sessions.
// Lines containing sessions look like:
//
//	\t<pid>.<name>\t(<timestamp>)\t(<state>)
//
// Some screen versions omit the timestamp field. Session names never contain
// whitespace, so we take the first field after the dot as the name and find
// the state in the last parenthesised field.
func parseScreenLs(output string) []screenSession {
	var sessions []screenSession
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		dotIdx := strings.Index(trimmed, ".")
		if dotIdx == -1 {
			continue
		}
		pid := trimmed[:dotIdx]
		if pid == "" || !allDigits(pid) {
			continue
		}
		rest := trimmed[dotIdx+1:]
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		state := ""
		for i := len(fields) - 1; i >= 1; i-- {
			f := fields[i]
			if strings.HasPrefix(f, "(") && strings.HasSuffix(f, ")") {
				state = f[1 : len(f)-1]
				break
			}
		}
		if name == "" {
			continue
		}
		sessions = append(sessions, screenSession{name: name, state: state})
	}
	return sessions
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// reconnectRestart finds worktree directories for each config that have no
// corresponding screen session and opens a new iTerm2 tab for each, running
// claude --continue inside the worktree.
func reconnectRestart(args []string) error {
	var entries []configEntry
	if len(args) == 1 {
		configPath, err := resolveConfigPath(args[0])
		if err != nil {
			return err
		}
		cfg, err := config.Parse(configPath)
		if err != nil {
			return err
		}
		entries = []configEntry{{path: configPath, cfg: cfg}}
	} else {
		var err error
		entries, err = loadAllConfigs()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(os.Stderr, "no configs found in ~/.clorchestrate/")
			return nil
		}
	}

	type repoTarget struct {
		remoteRepo string
		prefix     string
		tabColor   string
	}

	byServer := map[string][]repoTarget{}
	for _, e := range entries {
		color := config.ResolveTabColor(e.cfg.ITermTabColor, e.path)
		targets := []repoTarget{{e.cfg.RemoteRepo, e.cfg.WorktreePrefix, color}}
		for _, pkg := range e.cfg.Packages {
			if pkg.RemoteRepo == "" {
				continue
			}
			pkgColor := pkg.ITermTabColor
			if pkgColor == "" {
				pkgColor = e.cfg.ITermTabColor
			}
			targets = append(targets, repoTarget{
				pkg.RemoteRepo,
				pkg.WorktreePrefix,
				config.ResolveTabColor(pkgColor, e.path),
			})
		}
		for _, t := range targets {
			byServer[e.cfg.Server] = append(byServer[e.cfg.Server], t)
		}
	}

	for server, targets := range byServer {
		sessions, err := listScreenSessions(server)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not list sessions on %s: %v\n", server, err)
			continue
		}
		sessionNames := make(map[string]bool, len(sessions))
		for _, s := range sessions {
			sessionNames[s.name] = true
		}

		for _, t := range targets {
			if t.remoteRepo == "" {
				continue
			}
			repo := strings.TrimRight(t.remoteRepo, "/")
			lastSlash := strings.LastIndex(repo, "/")
			if lastSlash < 0 {
				continue
			}
			parent := repo[:lastSlash]
			prefix := t.prefix
			if prefix == "" {
				prefix = repo[lastSlash+1:]
			}

			dirs, err := listWorktreeDirs(server, parent, prefix)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not list worktrees for %s: %v\n", t.remoteRepo, err)
				continue
			}

			for _, dir := range dirs {
				base := dir[strings.LastIndex(dir, "/")+1:]
				if sessionNames[base] {
					fmt.Fprintf(os.Stderr, "skipping %s — session %q already exists\n", dir, base)
					continue
				}
				var remoteCmd string
				if server == "" {
					remoteCmd = fmt.Sprintf("screen -S %s bash -c 'cd %s && claude --continue'", base, dir)
				} else {
					remoteCmd = fmt.Sprintf(`ssh -t %s "screen -S %s bash -c 'cd %s && claude --continue'"`, server, base, dir)
				}
				fmt.Fprintf(os.Stderr, "starting session %q for %s\n", base, dir)
				if err := iterm.OpenTab(iterm.TabOptions{
					TabColorHex: t.tabColor,
					RemoteCmd:   remoteCmd,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not open tab for %s: %v\n", dir, err)
				}
			}
		}
	}

	return nil
}

// listWorktreeDirs returns directories under parent whose names start with
// prefix followed by "-". Runs locally when server is "".
func listWorktreeDirs(server, parent, prefix string) ([]string, error) {
	pattern := parent + "/" + prefix + "-*"
	shellCmd := fmt.Sprintf("ls -d %s 2>/dev/null", pattern)
	var out []byte
	if server == "" {
		out, _ = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, _ = exec.Command("ssh", server, shellCmd).Output()
	}
	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs, nil
}
