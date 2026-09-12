package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go worker and the browser plugin both read Copilot's markdown checklist, so
// one corpus is the executable specification that each side replays.
func TestCopilotChecklistConformance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "copilot_checklist_conformance.json"))
	require.NoError(t, err)
	var corpus struct {
		Cases []struct {
			Name      string `json:"name"`
			Checklist string `json:"checklist"`
			Expected  []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Cases)
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			items := parseCopilotChecklist(test.Checklist)
			require.Len(t, items, len(test.Expected))
			for index, want := range test.Expected {
				assert.Equal(t, want.Content, items[index].Content, "item %d content", index)
				assert.Equal(t, todoevents.StatusFromProviderWord(want.Status), items[index].Status, "item %d status", index)
				assert.Equal(t, want.Content, items[index].ActiveForm, "item %d active form", index)
			}
		})
	}
}
