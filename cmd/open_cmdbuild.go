package cmd

import (
	"fmt"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

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

// resolveClaudeCmd picks the session's claude-launch command: the command
// matching the benchmark label if one is set, else the command label, else the
// config default.
func resolveClaudeCmd(cfg *config.Config, opts openOptions) (string, error) {
	if opts.benchmarkLabel != "" {
		return cfg.CommandByLabel(opts.benchmarkLabel)
	}
	if opts.commandLabel != "" {
		return cfg.CommandByLabel(opts.commandLabel)
	}
	return cfg.DefaultClaudeCmd(), nil
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
