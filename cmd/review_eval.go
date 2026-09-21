package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chrisjensen/clorchestrate/prompts"
)

// runGroupedEvaluations groups rows by task key (worktree base name with label suffix stripped),
// then launches an evaluation agent for each directory in a group, informing it of the others.
// Evaluation results are written back into the rows slice in place.
func runGroupedEvaluations(rows []tokenTotals) {
	groups := map[string][]int{}
	for i, r := range rows {
		if r.skipEval {
			continue
		}
		groups[r.taskKey] = append(groups[r.taskKey], i)
	}

	for _, indices := range groups {
		groupRows := make([]tokenTotals, len(indices))
		for j, i := range indices {
			groupRows[j] = rows[i]
		}

		for j, i := range indices {
			target := rows[i]
			others := make([]tokenTotals, 0, len(groupRows)-1)
			for k, r := range groupRows {
				if k != j {
					others = append(others, r)
				}
			}

			evalOthers := make([]prompts.EvalOther, len(others))
			for i, o := range others {
				evalOthers[i] = prompts.EvalOther{
					Dir:         o.dir,
					Label:       o.label,
					ProjectsDir: claudeProjectsDir(o.homeDir, o.dir),
				}
			}
			prompt, err := prompts.Eval(prompts.EvalData{
				TargetDir:         target.dir,
				TargetLabel:       target.label,
				TargetProjectsDir: claudeProjectsDir(target.homeDir, target.dir),
				BaseBranch:        target.baseBranch,
				Others:            evalOthers,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: build eval prompt for %s: %v\n", target.worktree, err)
				continue
			}
			if err := writeEvalPrompt(target.server, target.worktree, prompt); err != nil {
				fmt.Fprintf(os.Stderr, "warning: write eval prompt for %s: %v\n", target.worktree, err)
				continue
			}

			rawOutput, err := runEvaluation(target.server, target.parent, target.worktree, target.claudeCmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: evaluate %s: %v\n", target.worktree, err)
				continue
			}

			jsonStr, prose, err := extractJSONAndProse(rawOutput)
			if err != nil {
				preview := strings.TrimSpace(rawOutput)
				if len(preview) > 300 {
					preview = preview[:300] + "..."
				}
				fmt.Fprintf(os.Stderr, "warning: parse evaluation output for %s: %v\n--- output ---\n%s\n---\n", target.worktree, err, preview)
				continue
			}

			var ev evalJSON
			if err := json.Unmarshal([]byte(jsonStr), &ev); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unmarshal evaluation JSON for %s: %v\n", target.worktree, err)
				continue
			}

			rows[i].eval = ev
			rows[i].prose = prose
		}

		computeCompleteness(rows, indices)
	}
}

func writeEvalPrompt(server, worktreeBase, prompt string) error {
	path := fmt.Sprintf("/tmp/clorchestrate-eval-%s.md", worktreeBase)
	if server == "" {
		return os.WriteFile(path, []byte(prompt), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write eval prompt on %s: %w", server, err)
	}
	return nil
}

func evalResultFile(worktreeBase string) string {
	return fmt.Sprintf("/tmp/clorchestrate-eval-%s.out", worktreeBase)
}

func evalScreenName(worktreeBase string) string {
	return "clorchestrate-eval-" + worktreeBase
}

// deleteEvalArtifacts removes the result file and kills any running screen session for a worktree.
func deleteEvalArtifacts(server, worktreeBase string) {
	resultFile := evalResultFile(worktreeBase)
	screenName := evalScreenName(worktreeBase)
	if server == "" {
		os.Remove(resultFile)
		return
	}
	shellCmd := fmt.Sprintf("rm -f %s; screen -S %s -X quit 2>/dev/null; true", resultFile, screenName)
	exec.Command("ssh", server, shellCmd).Run() //nolint:errcheck
}

// isRetryableAPIError returns true when the output is an API-level error that is
// worth retrying (e.g. 529 service overloaded). Other errors (auth, bad request)
// are not retried since they will not resolve on their own.
func isRetryableAPIError(output string) bool {
	return strings.Contains(output, "API Error: 529")
}

// runEvaluation launches the evaluation for a single worktree, retrying up to 3
// times if the result file contains a retryable API error (e.g. 529 overloaded).
func runEvaluation(server, parent, worktreeBase, claudeCmd string) (string, error) {
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := attemptEvaluation(server, parent, worktreeBase, claudeCmd)
		if err != nil {
			return "", err
		}
		if !isRetryableAPIError(result) {
			return result, nil
		}
		if attempt < maxAttempts {
			fmt.Fprintf(os.Stderr, "evaluation %s: 529 error (attempt %d/%d), retrying in 60s\n", worktreeBase, attempt, maxAttempts)
			deleteEvalArtifacts(server, worktreeBase)
			time.Sleep(60 * time.Second)
		}
	}
	return "", fmt.Errorf("evaluation %s: failed after %d attempts (529 service overloaded)", worktreeBase, maxAttempts)
}

// attemptEvaluation runs a single evaluation attempt using a detached screen session
// (remote) or backgrounded nohup process (local), writing output to a result file.
// On re-run it reuses a cached result file or reconnects to an in-progress session.
//
// --safe-mode disables CLAUDE.md, hooks, and other customizations for this
// invocation: the eval agent otherwise inherits the operator's own global
// CLAUDE.md (e.g. "run quality checks and commit before finishing"), which is
// instructions for an implementer, not a reviewer, and was causing eval
// agents to hedge or refuse instead of emitting the required JSON output.
func attemptEvaluation(server, parent, worktreeBase, claudeCmd string) (string, error) {
	promptPath := fmt.Sprintf("/tmp/clorchestrate-eval-%s.md", worktreeBase)
	resultFile := evalResultFile(worktreeBase)
	screenName := evalScreenName(worktreeBase)

	// Cached: result file already written by a previous (possibly interrupted) run.
	if remoteFileNonEmpty(server, resultFile) {
		fmt.Fprintf(os.Stderr, "evaluation %s: using cached result\n", worktreeBase)
		return readRemoteFile(server, resultFile)
	}

	// In-progress: screen session is still running from a dropped connection.
	if screenSessionRunning(server, screenName) {
		fmt.Fprintf(os.Stderr, "evaluation %s: reconnecting to running session\n", worktreeBase)
	} else {
		// Launch a new evaluation.
		fmt.Fprintf(os.Stderr, "launching evaluation for %s\n", worktreeBase)
		var cmd *exec.Cmd
		if server == "" {
			// nohup backgrounds the process so it survives terminal close / suspend.
			shellCmd := fmt.Sprintf(
				"nohup sh -c 'cd %s && %s --safe-mode --print \"$(cat %s)\" > %s 2>&1' >/dev/null 2>&1 &",
				parent, claudeCmd, promptPath, resultFile,
			)
			cmd = exec.Command("sh", "-c", shellCmd)
		} else {
			// screen -dm starts detached; bash -lc sources the login profile for PATH.
			screenCmd := fmt.Sprintf(
				"screen -dmS %s bash -lc 'cd %s && %s --safe-mode --print \"$(cat %s)\" > %s 2>&1'",
				screenName, parent, claudeCmd, promptPath, resultFile,
			)
			cmd = exec.Command("ssh", server, screenCmd)
		}
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("launch evaluation for %s: %w", worktreeBase, err)
		}
	}

	// Poll until the result file appears (local) or the screen session exits (remote).
	fmt.Fprintf(os.Stderr, "waiting for evaluation %s...\n", worktreeBase)
	for {
		time.Sleep(10 * time.Second)

		if server != "" && screenSessionRunning(server, screenName) {
			continue
		}

		if remoteFileNonEmpty(server, resultFile) {
			fmt.Fprintf(os.Stderr, "evaluation %s: complete\n", worktreeBase)
			return readRemoteFile(server, resultFile)
		}

		if server == "" {
			// Local process still running — keep polling until the file appears.
			continue
		}

		// Remote: screen session exited but no result file — process likely failed.
		return "", fmt.Errorf("evaluation %s: screen session exited without producing output", worktreeBase)
	}
}
