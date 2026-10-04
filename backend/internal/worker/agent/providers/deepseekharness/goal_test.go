package deepseekharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedGoalCall struct {
	method string
	args   map[string]json.RawMessage
}

func TestGoalSetPreservesObjectivesThatAreNativeCommandWords(t *testing.T) {
	t.Parallel()
	for _, objective := range []string{"clear", "pause", "resume", "edit", "edit the parser", "clear\nkeep this exact objective"} {
		t.Run(objective, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var calls []capturedGoalCall
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID      string `json:"rpcId"`
					Method  string `json:"method"`
					Payload struct {
						Args map[string]json.RawMessage `json:"args"`
					} `json:"payload"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				calls = append(calls, capturedGoalCall{method: request.Method, args: request.Payload.Args})
				mu.Unlock()
				result := map[string]any{"ok": true}
				switch request.Method {
				case "goals/get":
				case "goals/create":
					result["value"] = map[string]any{"ref": map[string]any{"id": "native-goal", "revision": 1}}
				default:
					result["value"] = map[string]any{"result": map[string]any{"kind": "success", "text": "Command accepted"}}
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": result}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.rpc.endpoint = endpoint
			_, err = a.PerformGoalAction(agent.GoalActionSet, objective)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, calls, 2)
			assert.Equal(t, "goals/get", calls[0].method)
			assert.Equal(t, "goals/create", calls[1].method)
			assert.JSONEq(t, `"native-root"`, string(calls[1].args["agentId"]))
			var request struct {
				Objective string `json:"objective"`
			}
			require.NoError(t, json.Unmarshal(calls[1].args["request"], &request))
			assert.Equal(t, objective, request.Objective)
		})
	}
}

func TestGoalProjectionRestoresTheNestedNativeSnapshot(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	raw := []byte(`{"goal":{"id":"native-goal","revision":3,"objective":"Keep the native objective.","phase":"paused","maxGoalRounds":20},"roundsStarted":0,"createdAt":1000,"updatedAt":2000}`)
	require.NoError(t, a.applyGoalProjection(raw, true))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "native-goal", goal.NativeID)
	assert.Equal(t, "Keep the native objective.", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.True(t, goal.Snapshot)
	require.NotNil(t, goal.Iterations)
	assert.Zero(t, *goal.Iterations)
}
