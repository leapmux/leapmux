package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiPlanApprovalConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../../testdata/pi_plan_control_conformance.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name     string          `json:"name"`
			Payload  json.RawMessage `json:"payload"`
			Expected bool            `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	for _, item := range fixture.Cases {
		t.Run(item.Name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, item.Expected, isPiPlanApproval(item.Payload))
			resolution := (piProvider{}).ResolveControlResponse(ControlResponseContext{RequestPayload: item.Payload})
			var context struct {
				PlanApproval bool `json:"planApproval"`
			}
			require.NoError(t, json.Unmarshal(resolution.RequestContext, &context))
			assert.Equal(t, item.Expected, context.PlanApproval)
		})
	}
}
