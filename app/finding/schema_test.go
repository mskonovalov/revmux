package finding

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaProps collects every property name declared anywhere in a JSON schema, so a test can
// ask what a schema exposes without matching on raw text and tripping over descriptions.
func schemaProps(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc))

	props := map[string]json.RawMessage{}
	var walk func(node any)
	walk = func(node any) {
		obj, ok := node.(map[string]any)
		if !ok {
			if arr, isArr := node.([]any); isArr {
				for _, item := range arr {
					walk(item)
				}
			}
			return
		}
		for key, val := range obj {
			if key == "properties" {
				if declared, isObj := val.(map[string]any); isObj {
					for name, spec := range declared {
						encoded, err := json.Marshal(spec)
						require.NoError(t, err)
						props[name] = encoded
					}
				}
			}
			walk(val)
		}
	}
	walk(doc)
	return props
}

func requiredFields(t *testing.T, raw json.RawMessage) map[string]bool {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal(raw, &doc))

	required := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, val := range typed {
				if key == "required" {
					if list, ok := val.([]any); ok {
						for _, name := range list {
							if s, isStr := name.(string); isStr {
								required[s] = true
							}
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(doc)
	return required
}

func allSchemas() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"finder":    FinderSchema(),
		"synthesis": SynthesisSchema(),
		"verify":    VerifySchema(),
	}
}

func TestSchemas_ValidJSON(t *testing.T) {
	for name, raw := range allSchemas() {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			assert.Equal(t, "object", doc["type"])
			assert.NotEmpty(t, doc["properties"])
		})
	}
}

func TestSchemas_NeverExposeSources(t *testing.T) {
	for name, raw := range allSchemas() {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, schemaProps(t, raw), "sources",
				"a field the model can fill is a field it will fill, and one agent naming itself twice is self-corroboration")
		})
	}
}

func TestFinderSchema_OmitsVerdictAndAssignedFields(t *testing.T) {
	props := schemaProps(t, FinderSchema())

	assert.NotContains(t, props, "verdict", "the finder does not produce verdicts, the verify stage does")
	assert.NotContains(t, props, "id", "ids are stamped in Go as <agent>-<n>, so model-supplied ids would collide")
	assert.NotContains(t, props, "tags")

	for _, want := range []string{"findings", "file", "line", "end_line", "severity", "confidence", "title", "body", "fix", "lenses"} {
		assert.Contains(t, props, want)
	}

	required := requiredFields(t, FinderSchema())
	assert.True(t, required["findings"])
	assert.True(t, required["lenses"], "a finding has to say which lens raised it")
	assert.False(t, required["end_line"], "end_line is optional, zero means a single line")
	assert.False(t, required["fix"])
}

func TestSynthesisSchema_ThreeListsAndMergedIDs(t *testing.T) {
	props := schemaProps(t, SynthesisSchema())

	for _, want := range []string{"findings", "open_questions", "pre_existing"} {
		assert.Contains(t, props, want, "synthesis returns a three-list shape the finder schema does not describe")
	}
	assert.Contains(t, props, "merged_ids", "Go derives the sources and lenses unions from these")
	assert.NotContains(t, props, "lenses", "lenses are unioned from the merged inputs, not restated by the model")
	assert.NotContains(t, props, "verdict")

	required := requiredFields(t, SynthesisSchema())
	for _, want := range []string{"findings", "open_questions", "pre_existing", "merged_ids"} {
		assert.True(t, required[want], "%s must be required", want)
	}
}

func TestVerifySchema_RequiresVerdict(t *testing.T) {
	props := schemaProps(t, VerifySchema())

	require.Contains(t, props, "verdict", "a verdict per finding is this stage's entire output")
	assert.Contains(t, props, "id")

	required := requiredFields(t, VerifySchema())
	assert.True(t, required["verdict"])
	assert.True(t, required["id"])

	var spec struct {
		Enum []string `json:"enum"`
	}
	require.NoError(t, json.Unmarshal(props["verdict"], &spec))
	assert.ElementsMatch(t, []string{string(Confirmed), string(Refined), string(Rejected), string(Immaterial), string(PreExisting)},
		spec.Enum, "unverified is set by revmux when the stage is skipped, so the model never returns it")

	for _, refinable := range []string{"severity", "confidence", "title", "body", "fix", "line", "end_line"} {
		assert.Contains(t, props, refinable, "a refined verdict rewrites %s", refinable)
	}
}

func TestSchemas_SeverityVocabularyMatchesGo(t *testing.T) {
	for name, raw := range allSchemas() {
		t.Run(name, func(t *testing.T) {
			props := schemaProps(t, raw)
			require.Contains(t, props, "severity")

			var spec struct {
				Enum []string `json:"enum"`
			}
			require.NoError(t, json.Unmarshal(props["severity"], &spec))
			want := make([]string, 0, len(allSeverities))
			for _, s := range allSeverities {
				want = append(want, string(s))
			}
			assert.ElementsMatch(t, want, spec.Enum)
		})
	}
}

func TestFinderSchema_MatchesRecordedCaptureFixture(t *testing.T) {
	fixture, err := os.ReadFile("../executor/testdata/finder-schema.json")
	require.NoError(t, err, "the prerequisite capture is recorded under this file")
	assert.Equal(t, string(fixture), string(FinderSchema()),
		"the fixture wins any disagreement: editing it invalidates a capture no agent can retake")
}

func TestSchemas_ReturnCopies(t *testing.T) {
	for name, raw := range allSchemas() {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, raw)
			raw[0] = 'X'
		})
	}

	for name, raw := range allSchemas() {
		assert.Equal(t, byte('{'), raw[0], "%s schema was mutated by a caller", name)
	}
}
