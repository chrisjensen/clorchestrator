package github

import (
	"testing"
)

func TestParseBranchFromOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			"standard gh output",
			"https://github.com/org/repo/tree/123-my-feature\n",
			"123-my-feature",
			true,
		},
		{
			"trailing newlines",
			"https://github.com/org/repo/tree/feature/123\n\n\n",
			"123",
			true,
		},
		{
			"empty output",
			"",
			"",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseBranchFromOutput(c.in)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatBranchName(t *testing.T) {
	got := FormatBranchName("feature/{issue}-{handle}", "42", "my-task")
	if got != "feature/42-my-task" {
		t.Errorf("got %q", got)
	}
}

func TestIsIssueClosed(t *testing.T) {
	origRun := runFunc
	t.Cleanup(func() { runFunc = origRun })

	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"open issue", "OPEN\n", false},
		{"closed issue", "CLOSED\n", true},
		{"merged PR", "MERGED\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runFunc = func(name string, args ...string) ([]byte, error) {
				return []byte(c.output), nil
			}
			got, err := IsIssueClosed("42", "org/repo")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestDevelopBranch_PassesArgs(t *testing.T) {
	var gotName string
	var gotArgs []string
	origRun := runFunc
	t.Cleanup(func() { runFunc = origRun })
	runFunc = func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("https://github.com/org/repo/tree/my-branch\n"), nil
	}

	branch, err := DevelopBranch(DevelopArgs{
		IssueNum:         "42",
		IssueRepo:        "org/issues",
		BranchRepo:       "org/branches",
		Base:             "main",
		BranchNameFormat: "feat/{issue}-{handle}",
		Handle:           "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "my-branch" {
		t.Errorf("branch = %q", branch)
	}
	if gotName != "gh" {
		t.Errorf("cmd = %q", gotName)
	}
	wantFlags := map[string]string{
		"--repo":        "org/issues",
		"--branch-repo": "org/branches",
		"--base":        "main",
		"--name":        "feat/42-task",
	}
	for flag, want := range wantFlags {
		found := false
		for i, a := range gotArgs {
			if a == flag && i+1 < len(gotArgs) && gotArgs[i+1] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s=%s in args %v", flag, want, gotArgs)
		}
	}
}
