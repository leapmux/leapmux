package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCompactionAdmission(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"accepted", "rejected", "uncertain admitted", "uncertain absent", "finished before receipt", "new activity before rejection"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			var runtime *openCodeSessionRuntime
			var submissions, recoveries atomic.Int32
			var inputID atomic.Value
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/session/ses_1/compact":
					submissions.Add(1)
					var body openCodeCompactionRequest
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "queue", body.Delivery)
					assert.NotEmpty(t, body.ID)
					inputID.Store(body.ID)
					switch outcome {
					case "rejected", "new activity before rejection":
						if outcome == "new activity before rejection" {
							runtime.setNativeActivity(true, false)
						}
						w.WriteHeader(http.StatusConflict)
					case "uncertain admitted", "uncertain absent":
						w.WriteHeader(http.StatusInternalServerError)
					default:
						if outcome == "finished before receipt" {
							runtime.setNativeActivity(false, false)
						}
						_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"compaction"}}`, body.ID)
						assert.NoError(t, err)
					}
				case "/api/session/ses_1/inbox":
					if outcome == "uncertain admitted" {
						_, err := fmt.Fprintf(w, `{"data":[{"id":%q,"sessionID":"ses_1","type":"compaction"}]}`, inputID.Load())
						assert.NoError(t, err)
					} else {
						_, _ = w.Write([]byte(`{"data":[]}`))
					}
				case "/api/session/ses_1/message":
					_, _ = w.Write([]byte(`{"data":[],"cursor":{}}`))
				default:
					t.Errorf("unexpected compaction request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			runtime = inputRuntime(t, rpc, &agenttest.Sink{})
			runtime.requestRecovery = func() { recoveries.Add(1) }
			err := runtime.compactNativeContext()
			switch outcome {
			case "rejected", "new activity before rejection":
				require.Error(t, err)
				assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
			case "uncertain absent":
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			default:
				require.NoError(t, err)
			}
			assert.EqualValues(t, 1, submissions.Load(), "compaction admission must not resubmit an uncertain request")
			expectedRecoveries := int32(0)
			if outcome == "uncertain admitted" || outcome == "uncertain absent" {
				expectedRecoveries = 1
			}
			assert.Equal(t, expectedRecoveries, recoveries.Load())
			expectedActive := outcome != "rejected" && outcome != "finished before receipt"
			expectedSteerable := outcome == "new activity before rejection"
			assert.Equal(t, agent.TurnState{Active: expectedActive, Steerable: expectedSteerable}, runtime.PublishTurnActive())
			if outcome == "accepted" {
				assert.ErrorIs(t, runtime.compactNativeContext(), agent.ErrAgentBusy)
				assert.EqualValues(t, 1, submissions.Load())
			}
		})
	}
}

func TestNativeCompactionRefusesRetiredOwnership(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	runtime := inputRuntime(t, rpc, &agenttest.Sink{})
	runtime.claimTurnSequence = func() uint64 { return 0 }
	assert.ErrorIs(t, runtime.compactNativeContext(), agent.ErrInputSessionChanged)
	assert.Zero(t, calls.Load())
}
