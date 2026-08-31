package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

const recipeYAML = `
description: test adapter
command: cursor-agent
args: [--print, --output-format, stream-json]
model-args: [--model, "{{MODEL}}"]
effort-args: [--effort, "{{EFFORT}}"]
schema-args: [--schema, "{{SCHEMA}}"]
prompt-suffix: |

  Schema:
  {{SCHEMA}}
output:
  event-field: type
  result-event: result
  result-field: result
  actual-model-field: model
  progress-events: [assistant, tool_call]
`

func writeRecipe(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	recipes := filepath.Join(dir, "executors")
	require.NoError(t, os.MkdirAll(recipes, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(recipes, name+".yaml"), []byte(body), 0o600))
	return recipes
}

func loadRecipe(t *testing.T) executor.Recipe {
	t.Helper()
	recipes, err := executor.LoadRecipes(writeRecipe(t, "cursor", recipeYAML))
	require.NoError(t, err)
	recipe, ok := recipes.Get("cursor")
	require.True(t, ok)
	return recipe
}

func TestLoadRecipes(t *testing.T) {
	t.Run("missing directory is an empty cookbook", func(t *testing.T) {
		recipes, err := executor.LoadRecipes(filepath.Join(t.TempDir(), "missing"))
		require.NoError(t, err)
		assert.Empty(t, recipes.Names())
	})

	t.Run("loads a recipe by file name", func(t *testing.T) {
		dir := writeRecipe(t, "cursor", recipeYAML)
		recipes, err := executor.LoadRecipes(dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"cursor"}, recipes.Names())
		recipe, ok := recipes.Get("cursor")
		require.True(t, ok)
		assert.Equal(t, "cursor-agent", recipe.Command)
		assert.Equal(t, filepath.Join(dir, "cursor.yaml"), recipe.Source)
	})

	tests := []struct {
		name, file, body, want string
	}{
		{"built-in cannot be replaced", "claude", recipeYAML, "built-in executor"},
		{"invalid name", "Cursor", recipeYAML, "invalid name"},
		{"unknown key", "cursor", recipeYAML + "surprise: true\n", "field surprise not found"},
		{"missing command", "cursor", "description: x\noutput: {event-field: type, result-event: result, result-field: result}\n", "command is required"},
		{"missing output contract", "cursor", "description: x\ncommand: agent\n" +
			"output: {event-field: type, result-event: result, result-field: result}\n", "prompt-suffix or schema-args is required"},
		{"unknown variable", "cursor", "description: x\ncommand: agent\nmodel-args: ['{{SCHEMA}}']\n" +
			"prompt-suffix: '{{SCHEMA}}'\noutput: {event-field: type, result-event: result, result-field: result}\n", "unsupported variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.LoadRecipes(writeRecipe(t, tt.file, tt.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCursorCookbookRecipe(t *testing.T) {
	recipes, err := executor.LoadRecipes(filepath.Join("..", "..", "cookbook", "executors"))
	require.NoError(t, err)
	recipe, ok := recipes.Get("cursor")
	require.True(t, ok)
	assert.Equal(t, "cursor-agent", recipe.Command)
	assert.Equal(t, []string{"assistant", "tool_call"}, recipe.Output.ProgressEvents)
	assert.Contains(t, recipe.PromptSuffix, "{{SCHEMA}}")
}

func TestRecipeRunner_Run(t *testing.T) {
	stream := []byte("{\"type\":\"system\",\"model\":\"cursor-large\"}\n" +
		"{\"type\":\"assistant\"}\n" +
		"{\"type\":\"result\",\"result\":\"before {\\\"findings\\\":[]} after\"}\n")
	path := writeFixture(t, stream)
	runner := fakeRunner("emit", path)
	sink := &mocks.EventSinkMock{EmitFunc: func(executor.Event) {}}
	raw := &bytes.Buffer{}
	recipe := loadRecipe(t)
	r := executor.NewRecipeRunner(recipe, runner, executor.Opts{})

	res, err := r.Run(context.Background(), executor.Request{
		Prompt: "review", Model: "cursor-large", Effort: "high",
		Schema: json.RawMessage(`{"type":"object"}`), RawOutput: raw,
	}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.JSONEq(t, `{"findings":[]}`, string(res.StructuredOutput))
	assert.Equal(t, "cursor-large", res.RequestedModel)
	assert.Equal(t, "cursor-large", res.ActualModel)
	assert.Equal(t, stream, raw.Bytes())

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "cursor-agent", call.Name)
	assert.Equal(t, []string{
		"--print", "--output-format", "stream-json",
		"--model", "cursor-large", "--effort", "high", "--schema", `{"type":"object"}`,
	}, call.Args)

	require.Len(t, sink.EmitCalls(), 1, "only the first configured progress event is announced")
	assert.Equal(t, executor.EventProgress, sink.EmitCalls()[0].Event.Kind)
	assert.Equal(t, "assistant", sink.EmitCalls()[0].Event.Text)
}

func TestRecipeRunner_acceptsObjectResult(t *testing.T) {
	stream := []byte("{\"type\":\"result\",\"result\":{\"findings\":[]}}\n")
	r := executor.NewRecipeRunner(loadRecipe(t), fakeRunner("emit", writeFixture(t, stream)), executor.Opts{})

	res, err := r.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.JSONEq(t, `{"findings":[]}`, string(res.StructuredOutput))
}
