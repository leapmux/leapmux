package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationProgressProbeSequences(t *testing.T) {
	t.Parallel()

	var fixture struct {
		Providers []struct {
			Name     string              `json:"name"`
			Events   []progressTestEvent `json:"events"`
			Expected ProgressSnapshot    `json:"expected"`
		} `json:"providers"`
	}
	raw, err := os.ReadFile("testdata/generation_progress.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Providers, 10)
	for _, provider := range fixture.Providers {
		provider := provider
		t.Run(provider.Name, func(t *testing.T) {
			t.Parallel()
			var counter ProgressCounter
			for _, event := range provider.Events {
				counter.Apply(event.update())
			}
			assert.Equal(t, provider.Expected, counter.Snapshot())
		})
	}
}

type progressTestEvent struct {
	Operation string `json:"operation"`
	Scope     string `json:"scope"`
	Text      string `json:"text"`
	Value     int64  `json:"value"`
	Minimum   bool   `json:"minimum"`
}

func (e progressTestEvent) update() ProgressUpdate {
	switch e.Operation {
	case "model_text":
		return ModelTextProgress(e.Scope, e.Text)
	case "native_tokens":
		return NativeTokenProgress(e.Scope, e.Value)
	case "output_delta":
		return OutputDeltaProgress(e.Scope, e.Value)
	case "output_total":
		return OutputTotalProgress(e.Scope, e.Value, e.Minimum)
	case "model_complete":
		return CompleteModelProgress(e.Scope)
	case "output_complete":
		return CompleteOutputProgress(e.Scope)
	case "reset":
		return ResetProgress()
	default:
		return ProgressUpdate{}
	}
}
