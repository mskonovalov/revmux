package archive

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

func TestCorpus_JSON(t *testing.T) {
	c := Corpus{
		Tasks: []taskStats{{
			ID:     "pr-123",
			Rounds: 2,
			Agents: []agentStats{{
				Name: "bugs+impl", Raised: 5, Survived: 4, Corroborated: 3,
				DegradedRounds: 1, Retries: 2, Tokens: 4986379,
			}},
			Lenses: []lensStats{{
				Name: "bugs", Raised: 3, Ambiguous: 2,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 1, finding.Unverified: 2},
			}},
			Stages: []stageFlow{{Name: "synthesis", In: 26, Out: 17}},
		}},
		Totals: taskStats{Rounds: 2},
	}

	data, err := json.Marshal(c)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	task := got["tasks"].([]any)[0].(map[string]any)

	t.Run("every field a caller reads is named in snake case", func(t *testing.T) {
		assert.Equal(t, []string{"agents", "id", "lenses", "rounds", "stages"}, keys(task))
		assert.Equal(t, "pr-123", task["id"])

		assert.Equal(t, []string{"corroborated", "degraded_rounds", "name", "raised", "retries", "survived", "tokens"},
			keys(task["agents"].([]any)[0].(map[string]any)))
		assert.Equal(t, []string{"ambiguous", "name", "raised", "verdicts"},
			keys(task["lenses"].([]any)[0].(map[string]any)))
		assert.Equal(t, []string{"in", "name", "out"}, keys(task["stages"].([]any)[0].(map[string]any)))
	})

	t.Run("verdict counts are keyed by the vocabulary a report carries", func(t *testing.T) {
		lens := task["lenses"].([]any)[0].(map[string]any)
		assert.Equal(t, map[string]any{"confirmed": 1.0, "unverified": 2.0}, lens["verdicts"])
	})

	t.Run("totals carry no id, since they are every task at once", func(t *testing.T) {
		assert.Equal(t, []string{"agents", "lenses", "rounds", "stages"}, keys(got["totals"].(map[string]any)))
	})
}

func keys(obj map[string]any) []string { return slices.Sorted(maps.Keys(obj)) }
