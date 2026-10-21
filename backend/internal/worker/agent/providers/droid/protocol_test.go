package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeParamsUsesTheNativeModelField(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"", "custom:chosen-model"} {
		t.Run(model, func(t *testing.T) {
			raw, err := json.Marshal(initializeParams{Cwd: "/work", MachineID: "machine", Model: model})
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			assert.NotContains(t, fields, "model", "Droid reads modelId during initialization")
			if model == "" {
				assert.NotContains(t, fields, "modelId")
			} else {
				assert.Equal(t, model, fields["modelId"])
			}
		})
	}
}
