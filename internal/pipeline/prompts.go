package pipeline

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed prompts
var promptFS embed.FS

var (
	systemTmpl *template.Template
	planTmpl   *template.Template
	answerTmpl *template.Template
)

func init() {
	systemTmpl = mustParse("system", "prompts/system.md")
	planTmpl = mustParse("plan", "prompts/plan.md")
	answerTmpl = mustParse("answer", "prompts/answer.md")
}

func mustParse(name, path string) *template.Template {
	data, err := promptFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("pipeline: embed %s: %v", path, err))
	}
	t, err := template.New(name).Parse(string(data))
	if err != nil {
		panic(fmt.Sprintf("pipeline: parse template %s: %v", name, err))
	}
	return t
}

// RenderSystem renders the system prompt with available source types and modules.
func RenderSystem(data SystemPromptData) (string, error) {
	return render(systemTmpl, data)
}

// RenderPlan renders the plan-phase user instructions.
func RenderPlan(data PlanPromptData) (string, error) {
	return render(planTmpl, data)
}

// RenderAnswer renders the answer-phase user instructions with fetched sources.
func RenderAnswer(data AnswerPromptData) (string, error) {
	return render(answerTmpl, data)
}

// SystemPromptData is the template data for system.md.
type SystemPromptData struct {
	SourceTypes string // formatted list of source types
	Modules     string // formatted list of module names + scopes
}

// PlanPromptData is the template data for plan.md.
type PlanPromptData struct{}

// AnswerPromptData is the template data for answer.md.
type AnswerPromptData struct {
	Sources string // formatted fetched content block
}

func render(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("pipeline: render %s: %w", t.Name(), err)
	}
	return buf.String(), nil
}
