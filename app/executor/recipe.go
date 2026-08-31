package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	recipeName    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	recipeVar     = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
	builtinRunner = []string{"claude", "codex"}
)

// Recipe describes a user-maintained model CLI adapter. Recipes are loaded only from the operator's
// config directory; project configuration never gets to choose an executable.
type Recipe struct {
	Name         string       `yaml:"-" json:"name"`
	Description  string       `yaml:"description" json:"description"`
	Command      string       `yaml:"command" json:"command"`
	Args         []string     `yaml:"args" json:"args"`
	ModelArgs    []string     `yaml:"model-args" json:"model_args,omitempty"`
	EffortArgs   []string     `yaml:"effort-args" json:"effort_args,omitempty"`
	SchemaArgs   []string     `yaml:"schema-args" json:"schema_args,omitempty"`
	PromptSuffix string       `yaml:"prompt-suffix" json:"prompt_suffix,omitempty"`
	Output       RecipeOutput `yaml:"output" json:"output"`
	Source       string       `yaml:"-" json:"source"`
}

// RecipeOutput identifies the terminal event in a JSONL stream and the field holding the model's
// answer. ProgressEvents are event-field values that prove the model has started working.
type RecipeOutput struct {
	EventField       string   `yaml:"event-field" json:"event_field"`
	ResultEvent      string   `yaml:"result-event" json:"result_event"`
	ResultField      string   `yaml:"result-field" json:"result_field"`
	ActualModelField string   `yaml:"actual-model-field" json:"actual_model_field,omitempty"`
	ProgressEvents   []string `yaml:"progress-events" json:"progress_events,omitempty"`
}

// Recipes is the set of adapters loaded from one operator-owned executors directory.
type Recipes struct {
	items map[string]Recipe
}

// LoadRecipes reads *.yaml recipes from dir. A missing directory means no added executors.
func LoadRecipes(dir string) (Recipes, error) {
	out := Recipes{items: map[string]Recipe{}}
	if dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return Recipes{}, fmt.Errorf("read executor recipes %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		path := filepath.Join(dir, entry.Name())
		if !recipeName.MatchString(name) {
			return Recipes{}, fmt.Errorf("executor recipe %s: invalid name %q", path, name)
		}
		if slices.Contains(builtinRunner, name) {
			return Recipes{}, fmt.Errorf("executor recipe %s: %q is a built-in executor", path, name)
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // the operator explicitly owns this config directory
		if readErr != nil {
			return Recipes{}, fmt.Errorf("read executor recipe %s: %w", path, readErr)
		}
		var recipe Recipe
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if decodeErr := dec.Decode(&recipe); decodeErr != nil {
			return Recipes{}, fmt.Errorf("parse executor recipe %s: %w", path, decodeErr)
		}
		recipe.Name, recipe.Source = name, path
		if validateErr := recipe.validate(); validateErr != nil {
			return Recipes{}, fmt.Errorf("executor recipe %s: %w", path, validateErr)
		}
		out.items[name] = recipe
	}
	return out, nil
}

// Names returns recipe names in stable order.
func (r Recipes) Names() []string {
	out := make([]string, 0, len(r.items))
	for name := range r.items {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// All returns recipes in stable name order for introspection.
func (r Recipes) All() []Recipe {
	out := make([]Recipe, 0, len(r.items))
	for _, name := range r.Names() {
		out = append(out, r.items[name])
	}
	return out
}

// Get returns the named recipe.
func (r Recipes) Get(name string) (Recipe, bool) {
	recipe, ok := r.items[name]
	return recipe, ok
}

func (r Recipe) validate() error {
	switch {
	case strings.TrimSpace(r.Description) == "":
		return errors.New("description is required")
	case strings.TrimSpace(r.Command) == "":
		return errors.New("command is required")
	case strings.TrimSpace(r.Output.EventField) == "":
		return errors.New("output.event-field is required")
	case strings.TrimSpace(r.Output.ResultEvent) == "":
		return errors.New("output.result-event is required")
	case strings.TrimSpace(r.Output.ResultField) == "":
		return errors.New("output.result-field is required")
	case strings.TrimSpace(r.PromptSuffix) == "" && len(r.SchemaArgs) == 0:
		return errors.New("prompt-suffix or schema-args is required")
	}

	groups := []struct {
		name    string
		values  []string
		allowed []string
	}{
		{"args", r.Args, nil},
		{"model-args", r.ModelArgs, []string{"MODEL"}},
		{"effort-args", r.EffortArgs, []string{"EFFORT"}},
		{"schema-args", r.SchemaArgs, []string{"SCHEMA"}},
		{"prompt-suffix", []string{r.PromptSuffix}, []string{"SCHEMA"}},
	}
	for _, group := range groups {
		for _, value := range group.values {
			for _, match := range recipeVar.FindAllStringSubmatch(value, -1) {
				if !slices.Contains(group.allowed, match[1]) {
					return fmt.Errorf("%s contains unsupported variable %s", group.name, match[0])
				}
			}
		}
	}
	return nil
}

// RecipeRunner supervises one recipe through the same process and timeout machinery as built-ins.
type RecipeRunner struct {
	proc
	recipe Recipe
}

// NewRecipeRunner builds a generic JSONL executor from an operator-owned recipe.
func NewRecipeRunner(recipe Recipe, runner CommandRunner, opts Opts) *RecipeRunner {
	return &RecipeRunner{proc: newProc(recipe.Command, runner, opts), recipe: recipe}
}

// Run executes a recipe and extracts structured output from its configured terminal event.
func (r *RecipeRunner) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += r.recipe.promptSuffix(req.Schema)
	spec := runSpec{
		argv: r.recipe.argv(req),
		sink: sink,
		parse: func(ctx context.Context, stream io.Reader) Result {
			return r.parseJSONL(ctx, stream, sink)
		},
	}
	res, err := r.run(ctx, req, spec)
	res.RequestedModel = req.Model
	return res, err
}

func (r Recipe) argv(req Request) []string {
	out := append([]string(nil), r.Args...)
	if req.Model != "" {
		out = append(out, renderRecipe(r.ModelArgs, "MODEL", req.Model)...)
	}
	if req.Effort != "" {
		out = append(out, renderRecipe(r.EffortArgs, "EFFORT", req.Effort)...)
	}
	if len(req.Schema) > 0 {
		out = append(out, renderRecipe(r.SchemaArgs, "SCHEMA", string(req.Schema))...)
	}
	return out
}

func (r Recipe) promptSuffix(schema json.RawMessage) string {
	return strings.ReplaceAll(r.PromptSuffix, "{{SCHEMA}}", string(schema))
}

// RenderPromptSuffix returns the recipe text appended to the composed prompt. The pipeline records the
// same text before Run appends it to the live request.
func (r Recipe) RenderPromptSuffix(schema json.RawMessage) string { return r.promptSuffix(schema) }

func renderRecipe(values []string, variable, value string) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = strings.ReplaceAll(values[i], "{{"+variable+"}}", value)
	}
	return out
}

func (r *RecipeRunner) parseJSONL(ctx context.Context, stream io.Reader, sink EventSink) Result {
	res := Result{}
	progressed := false
	_ = r.readLines(ctx, stream, func(line string) {
		var event map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}

		kind := stringField(event[r.recipe.Output.EventField])
		if !progressed && slices.Contains(r.recipe.Output.ProgressEvents, kind) {
			progressed = true
			r.emit(sink, Event{Kind: EventProgress, Text: kind})
		}
		if field := r.recipe.Output.ActualModelField; field != "" {
			if model := stringField(event[field]); model != "" {
				res.ActualModel = model
			}
		}
		if kind != r.recipe.Output.ResultEvent {
			return
		}

		answer := event[r.recipe.Output.ResultField]
		if text := stringField(answer); text != "" {
			if object, err := extractJSONObject(text); err == nil {
				res.StructuredOutput = object
			}
			return
		}
		if validJSONObject(answer) {
			res.StructuredOutput = append(json.RawMessage(nil), answer...)
		}
	})
	return res
}

func stringField(raw json.RawMessage) string {
	var out string
	_ = json.Unmarshal(raw, &out)
	return out
}

func validJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{") && json.Valid(raw)
}

func extractJSONObject(raw string) (json.RawMessage, error) {
	for i, ch := range raw {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(raw[i:])).Decode(&out)
		if err == nil && validJSONObject(out) {
			return out, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil, errors.New("no JSON object in recipe output")
}
