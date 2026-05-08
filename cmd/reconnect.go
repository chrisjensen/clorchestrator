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
	cmd := &cobra.Command{
		Use:   "reconnect [config]",
		Short: "reconnect detached screen sessions",
		Long: `Reconnect detached or attached screen sessions in new iTerm2 tabs.

Without <config>: reconnect all sessions for every config in ~/.clorchestrate/.
With <config>: reconnect only sessions belonging to that config.

By default only Detached sessions are reattached (via 'screen -r'), so
sessions already open in another tab are left alone. Pass --force to
forcibly reattach any matching session (via 'screen -dr'), detaching any
currently-connected client.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeConfigPaths,
		RunE: func(cmd *cobra.Command, args []string) error {
			return reconnectRun(args, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "forcibly reattach matching sessions using 'screen -dr'")
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
				if pkg.WorktreePrefix == "" {
					continue
				}
				pkgColor := pkg.ITermTabColor
				if pkgColor == "" {
					pkgColor = e.cfg.ITermTabColor
				}
				matchers = append(matchers, matcher{
					prefix:   pkg.WorktreePrefix + "-",
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
