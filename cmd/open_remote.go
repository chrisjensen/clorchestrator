package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/conffile"
	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/prompts"
)

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

// buildTaskConf assembles the task conf the checkout script consumes. In hive
// mode the checkout script creates the run dir and copies task.md there: a
// worker gets its worktree at <runDir>/<label>; the coordinator has no
// worktree (empty WorktreeDir + empty Branch => run-dir-only setup).
func buildTaskConf(cfg *config.Config, mode Mode, opts openOptions, branch, issueNum, worktreeDir string) conffile.TaskConf {
	tc := conffile.TaskConf{
		Branch:         branch,
		RemoteRepo:     cfg.RemoteRepo,
		WorktreePrefix: cfg.WorktreePrefix,
		PostSetupCmd:   cfg.PostSetupCmd,
	}
	if opts.hiveRole != "" {
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
	return tc
}

// buildTaskBody renders the task body (issue ref / description + planning
// context) a session works from: the full issue prompt for a task, or a
// planning-context prompt for hive sessions and label worktree sessions with
// extra context. Returns "" when there is nothing to prompt with.
func buildTaskBody(cfg *config.Config, mode Mode, opts openOptions, issueNum string) (string, error) {
	if mode == ModeFullTask {
		p, err := prompts.Prompt(prompts.PromptData{
			IssueNum:        issueNum,
			IssueRepo:       cfg.IssueRepo,
			IssueURL:        fmt.Sprintf("https://github.com/%s/issues/%s", cfg.IssueRepo, issueNum),
			PlanningContext: cfg.PlanningContext,
			ExtraContext:    opts.extraContext,
		})
		if err != nil {
			return "", fmt.Errorf("building issue prompt: %w", err)
		}
		return p, nil
	}
	if opts.hiveRole != "" || (mode == ModeWorktree && (opts.extraContext != "" || opts.benchmarkLabel != "")) {
		p, err := prompts.Prompt(prompts.PromptData{
			PlanningContext: cfg.PlanningContext,
			Description:     opts.extraContext,
		})
		if err != nil {
			return "", fmt.Errorf("building task prompt: %w", err)
		}
		return p, nil
	}
	return "", nil
}

// sessionPromptFile writes the session's launch prompt file, and for hive
// workers the shared task.md. A worker's launch prompt is its task body (so
// the chat history shows what it was asked to do) plus an instruction to work
// it via the hive-worker skill; the coordinator has no task body, only
// /hive-coordinate <base>. Only workers write task.md: they run first and
// (for issue tasks) render the full issue prompt, whereas the coordinator has
// no issue and would otherwise clobber the run dir's task.md with an empty
// body. Non-hive benchmark sessions get a .clorchestrate-done instruction
// appended. Reports whether a prompt file was written.
func sessionPromptFile(cfg *config.Config, opts openOptions, slug, taskBody string) (bool, error) {
	if opts.hiveRole != "" {
		if taskBody != "" && opts.hiveRole == hiveRoleWorker {
			if err := writeTaskMD(cfg.Server, slug, taskBody); err != nil {
				return false, err
			}
		}
		launchPrompt := hiveLaunchPrompt(opts.hiveRole, taskBody, opts.coordinatorBase)
		if err := writePromptFile(cfg.Server, slug, launchPrompt); err != nil {
			return false, err
		}
		return true, nil
	}
	prompt := taskBody
	if opts.benchmarkLabel != "" && prompt != "" {
		prompt += "\n- Once committed and any quality checks have been completed, touch the file .clorchestrate-done in the working directory.\n" +
			"- .clorchestrate-done MUST NOT be committed — it must remain a local-only file. Add it to .gitignore if necessary.\n"
	}
	if prompt == "" {
		return false, nil
	}
	if err := writePromptFile(cfg.Server, slug, prompt); err != nil {
		return false, err
	}
	return true, nil
}

// runSessionSetup stages everything the session needs on the target machine:
// synced scripts, the task conf, the prompt files, and the checkout itself.
// It records plan.wrotePrompt for the later followup step.
func runSessionSetup(cfg *config.Config, mode Mode, opts openOptions, handle, branch, issueNum string, plan *openSessionPlan) error {
	location := cfg.Server
	if location == "" {
		location = "local"
	}
	fmt.Fprintf(os.Stderr, "Running setup for %s on %s…\n", handle, location)
	if err := syncServerScripts(cfg.Server); err != nil {
		return err
	}
	tc := buildTaskConf(cfg, mode, opts, branch, issueNum, plan.worktreeDir)
	if err := writeTaskConf(cfg.Server, plan.slug, tc); err != nil {
		return err
	}
	if !opts.noClaude {
		taskBody, err := buildTaskBody(cfg, mode, opts, issueNum)
		if err != nil {
			return fmt.Errorf("building task prompt: %w", err)
		}
		wrote, err := sessionPromptFile(cfg, opts, plan.slug, taskBody)
		if err != nil {
			return err
		}
		plan.wrotePrompt = wrote
	}
	if err := runSetup(cfg.Server, plan.slug); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Setup complete — launching session.")
	return nil
}
