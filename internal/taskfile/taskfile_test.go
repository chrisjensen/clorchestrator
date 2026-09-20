package taskfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParse_FullIssueURL(t *testing.T) {
	path := writeTemp(t, `## my-task
https://github.com/org/repo/issues/123

Some context.
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Handle != "my-task" {
		t.Errorf("Handle = %q", tasks[0].Handle)
	}
	if tasks[0].IssueNum != "123" {
		t.Errorf("IssueNum = %q", tasks[0].IssueNum)
	}
	if !strings.Contains(tasks[0].ExtraContext, "Some context") {
		t.Errorf("ExtraContext = %q", tasks[0].ExtraContext)
	}
}

func TestParse_ShorthandIssue(t *testing.T) {
	path := writeTemp(t, `## handle
org/repo#456
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].IssueNum != "456" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestParse_BareIssue(t *testing.T) {
	path := writeTemp(t, `## handle
#789
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].IssueNum != "789" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestParse_BaseOverride(t *testing.T) {
	path := writeTemp(t, `## handle
#100
base: release/1.2
Extra line of context.
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	if tasks[0].BaseBranch != "release/1.2" {
		t.Errorf("BaseBranch = %q", tasks[0].BaseBranch)
	}
	if strings.Contains(tasks[0].ExtraContext, "base:") {
		t.Errorf("ExtraContext should not contain 'base:' line: %q", tasks[0].ExtraContext)
	}
	if !strings.Contains(tasks[0].ExtraContext, "Extra line of context.") {
		t.Errorf("ExtraContext missing content: %q", tasks[0].ExtraContext)
	}
}

func TestParse_MultipleEntries(t *testing.T) {
	path := writeTemp(t, `## first
#1
context one

## second
org/r#2
context two
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Handle != "first" || tasks[0].IssueNum != "1" {
		t.Errorf("tasks[0] = %+v", tasks[0])
	}
	if tasks[1].Handle != "second" || tasks[1].IssueNum != "2" {
		t.Errorf("tasks[1] = %+v", tasks[1])
	}
}

func TestParse_NoIssueIncluded(t *testing.T) {
	path := writeTemp(t, `## no-issue
Just context without any issue ref.

## has-issue
#42
ok
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Handle != "no-issue" {
		t.Errorf("tasks[0].Handle = %q", tasks[0].Handle)
	}
	if tasks[0].IssueNum != "" {
		t.Errorf("tasks[0].IssueNum = %q, want empty", tasks[0].IssueNum)
	}
	if !strings.Contains(tasks[0].ExtraContext, "Just context") {
		t.Errorf("tasks[0].ExtraContext = %q", tasks[0].ExtraContext)
	}
	if tasks[1].Handle != "has-issue" || tasks[1].IssueNum != "42" {
		t.Errorf("tasks[1] = %+v", tasks[1])
	}
}

func TestParse_NoIssueOnlyHandle(t *testing.T) {
	path := writeTemp(t, `## my-feature
Implement the new widget system.
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Handle != "my-feature" {
		t.Errorf("Handle = %q", tasks[0].Handle)
	}
	if tasks[0].IssueNum != "" {
		t.Errorf("IssueNum = %q, want empty", tasks[0].IssueNum)
	}
	if !strings.Contains(tasks[0].ExtraContext, "Implement the new widget system") {
		t.Errorf("ExtraContext = %q", tasks[0].ExtraContext)
	}
}

func TestParse_HeadingNoSpace(t *testing.T) {
	path := writeTemp(t, "##handle\n#1\n")
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Handle != "handle" {
		t.Errorf("Handle = %q, want %q", tasks[0].Handle, "handle")
	}
}

func TestParse_HeadingExtraSpaces(t *testing.T) {
	path := writeTemp(t, "##  handle  \n#1\n")
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Handle != "handle" {
		t.Errorf("Handle = %q, want %q", tasks[0].Handle, "handle")
	}
}

func TestParse_CommandList(t *testing.T) {
	path := writeTemp(t, `## handle
#100
command: zai, claude ,
Some context.
`)
	tasks, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	want := []string{"zai", "claude"}
	if len(tasks[0].Commands) != len(want) {
		t.Fatalf("Commands = %#v, want %#v", tasks[0].Commands, want)
	}
	for i, c := range want {
		if tasks[0].Commands[i] != c {
			t.Errorf("Commands[%d] = %q, want %q", i, tasks[0].Commands[i], c)
		}
	}
	if strings.Contains(tasks[0].ExtraContext, "command:") {
		t.Errorf("command line leaked into ExtraContext: %q", tasks[0].ExtraContext)
	}
}

func TestNormalizeIssueArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"42", "42"},
		{"#42", "42"},
		{"org/repo#42", "42"},
		{"https://github.com/org/repo/issues/42", "42"},
	}
	for _, c := range cases {
		got, err := NormalizeIssueArg(c.in)
		if err != nil {
			t.Errorf("NormalizeIssueArg(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeIssueArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
