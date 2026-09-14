// Package branchdetect auto-detects a repo's default branch when no
// default_base/base_branch is configured, so branching and diffing target
// the repo's actual default (main, master, or anything else) instead of an
// assumed one.
package branchdetect

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/github"
)

const fallback = "main"

// runFunc is the subprocess runner for the git fallback path. Replaced in tests.
var runFunc = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.Output()
}

// Resolver detects and caches default branches per repo so a batch run with
// many tasks against the same repo only shells out once.
type Resolver struct {
	cache map[string]string
}

// NewResolver returns a Resolver with an empty cache.
func NewResolver() *Resolver {
	return &Resolver{cache: map[string]string{}}
}

// Resolve returns the default branch for the repo described by server,
// remoteRepo (a filesystem path, possibly on server), and branchRepo/issueRepo
// (gh-style "owner/repo" slugs). Tries gh first (when a repo slug is known),
// then git (when a remote path is known), then falls back to "main".
func (r *Resolver) Resolve(server, remoteRepo, branchRepo, issueRepo string) (string, error) {
	key := server + "|" + remoteRepo + "|" + branchRepo + "|" + issueRepo
	if b, ok := r.cache[key]; ok {
		return b, nil
	}

	branch, warning := detect(server, remoteRepo, branchRepo, issueRepo)
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	r.cache[key] = branch
	return branch, nil
}

func detect(server, remoteRepo, branchRepo, issueRepo string) (string, string) {
	repo := branchRepo
	if repo == "" {
		repo = issueRepo
	}
	if repo != "" {
		if branch, err := github.DefaultBranch(repo); err == nil && branch != "" {
			return branch, ""
		}
	}

	if remoteRepo != "" {
		if branch, err := gitDefaultBranch(server, remoteRepo); err == nil && branch != "" {
			return branch, ""
		}
	}

	return fallback, fmt.Sprintf("warning: could not auto-detect default branch for %s — assuming %q", remoteRepoOrRepo(remoteRepo, repo), fallback)
}

func remoteRepoOrRepo(remoteRepo, repo string) string {
	if remoteRepo != "" {
		return remoteRepo
	}
	return repo
}

// gitDefaultBranch resolves the default branch by asking git which branch
// origin/HEAD points at, run on server (over ssh) when set, else locally.
func gitDefaultBranch(server, remoteRepo string) (string, error) {
	shellCmd := fmt.Sprintf(`git -C %s symbolic-ref --short refs/remotes/origin/HEAD`, remoteRepo)
	var out []byte
	var err error
	if server == "" {
		out, err = runFunc("sh", "-c", shellCmd)
	} else {
		out, err = runFunc("ssh", server, shellCmd)
	}
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	branch = strings.TrimPrefix(branch, "origin/")
	if branch == "" {
		return "", fmt.Errorf("git symbolic-ref: empty output")
	}
	return branch, nil
}
