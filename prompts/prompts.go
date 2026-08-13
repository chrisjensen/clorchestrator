package prompts

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed prompt.txt
var promptTmpl string

//go:embed eval.txt
var evalTmpl string

// PromptData covers both task and issue modes.
// Set IssueNum/IssueRepo/IssueURL/ExtraContext for a GitHub issue; set Description for a free-form task.
type PromptData struct {
	PlanningContext string
	// Issue mode
	IssueNum  string
	IssueRepo string
	IssueURL  string
	ExtraContext string
	// Task mode
	Description string
}

type EvalOther struct {
	Dir         string
	Label       string
	ProjectsDir string
}

type EvalData struct {
	TargetDir         string
	TargetLabel       string
	TargetProjectsDir string
	Others            []EvalOther
}

func Prompt(data PromptData) (string, error) {
	return render(promptTmpl, data)
}

func Eval(data EvalData) (string, error) {
	return render(evalTmpl, data)
}

func render(tmpl string, data any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
