package service

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureSessionObservationForTest(t *testing.T, services agent.ProviderServices) agent.CapturedSessionObservationWriter {
	t.Helper()
	captured := agent.CaptureTranscript(services, agent.MessageContent{AgentSessionID: services.MessageSessionID()}, agent.SpanInfo{})
	writer, err := services.SessionObservationWriterFor(captured)
	require.NoError(t, err)
	return writer
}

func TestCapturedSessionObservation_RejectsForeignCaptures(t *testing.T) {
	t.Parallel()
	svc, _, _, services, _ := goalPublicationFixture(t)
	wrong := agent.CaptureTranscript(services, agent.MessageContent{AgentSessionID: "another-session"}, agent.SpanInfo{})
	_, err := services.SessionObservationWriterFor(wrong)
	require.Error(t, err)
	captured := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	replacement := newTestRootOutputSink(t, svc.Output, sink.agentID, sink.agentProvider)
	_, err = replacement.SessionObservationWriterFor(captured)
	require.Error(t, err)
}

func TestCapturedSessionObservation_PreservesNativeAuthorityAcrossOrdinaryTurns(t *testing.T) {
	t.Parallel()
	svc, _, _, services, _ := goalPublicationFixture(t)
	writer := captureSessionObservationForTest(t, services)
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	services.SetTurnState(agent.TurnState{Active: false}, 1)
	services.UpdateSessionID(services.MessageSessionID())
	assert.True(t, writer.PublishSessionInfo(map[string]interface{}{"ordinary-turn": 42}))
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	assert.Equal(t, float64(42), replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents(sink.agentID)[0])["ordinary-turn"])
}

func TestCapturedSessionObservation_ExpiresAfterNativeReplacement(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"different ID", "A-B-A", "fresh same ID"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			svc, _, _, services, _ := goalPublicationFixture(t)
			writer := captureSessionObservationForTest(t, services)
			originalID := services.MessageSessionID()
			if replacement == "fresh same ID" {
				services.AdoptNativeSession(originalID)
			} else {
				services.UpdateSessionID("replacement")
				if replacement == "A-B-A" {
					services.UpdateSessionID(originalID)
				}
			}
			before, err := svc.Queries.GetAgentByID(t.Context(), "goal-publication-owner")
			require.NoError(t, err)
			writer.PersistSettingsRefresh(optionmap.Map{agent.OptionIDEffort: "old"})
			assert.False(t, writer.PublishSessionInfo(map[string]interface{}{"expired": true}))
			after, err := svc.Queries.GetAgentByID(t.Context(), "goal-publication-owner")
			require.NoError(t, err)
			assert.Equal(t, before.Options, after.Options)
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			assert.NotContains(t, sink.sessionInfoSnapshot(), "expired")
		})
	}
}

func TestCapturedSessionObservation_NewerObservationExpiresOlderEffects(t *testing.T) {
	t.Parallel()
	svc, _, _, services, _ := goalPublicationFixture(t)
	old := captureSessionObservationForTest(t, services)
	newer := captureSessionObservationForTest(t, services)
	newer.PersistSettingsRefresh(optionmap.Map{agent.OptionIDEffort: "high"})
	assert.True(t, newer.PublishSessionInfo(map[string]interface{}{"latest": "new"}))
	old.PersistSettingsRefresh(optionmap.Map{agent.OptionIDEffort: "low"})
	assert.False(t, old.PublishSessionInfo(map[string]interface{}{"latest": "old"}))
	old.Retire()
	old.Retire()
	assert.True(t, newer.PublishSessionInfo(map[string]interface{}{"latest": "newest"}))
	row, err := svc.Queries.GetAgentByID(t.Context(), "goal-publication-owner")
	require.NoError(t, err)
	assert.Equal(t, "high", parseOptions(row.Options)[agent.OptionIDEffort])
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	assert.Equal(t, "newest", replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents(sink.agentID)[0])["latest"])
}

type sessionObservationMarshal struct{ before func() }

func (value sessionObservationMarshal) MarshalJSON() ([]byte, error) {
	value.before()
	return json.Marshal(7)
}

func TestCapturedSessionObservation_ExpiresBeforeCacheAdmission(t *testing.T) {
	t.Parallel()
	svc, _, _, services, _ := goalPublicationFixture(t)
	writer := captureSessionObservationForTest(t, services)
	accepted := writer.PublishSessionInfo(map[string]interface{}{"prepared": sessionObservationMarshal{before: func() {
		services.UpdateSessionID("replacement")
	}}})
	assert.False(t, accepted)
	sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
	assert.NotContains(t, sink.sessionInfoSnapshot(), "prepared")
}

func TestCapturedSessionObservation_RetirementSeparatesShutdownFromNativeReplacement(t *testing.T) {
	t.Parallel()
	for _, nativeReplacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "native replacement"}[nativeReplacement], func(t *testing.T) {
			t.Parallel()
			svc, _, _, services, _ := goalPublicationFixture(t)
			writer := captureSessionObservationForTest(t, services)
			assert.True(t, writer.PublishSessionInfo(map[string]interface{}{
				contracts.SessionInfoKeyContextUsage: map[string]interface{}{"context_tokens": 500},
				contracts.SessionInfoKeyTotalCostUsd: 0.42,
				contracts.SessionInfoKeyRateLimits:   map[string]interface{}{"remaining": 10},
			}))
			if nativeReplacement {
				services.AdoptNativeSession(services.MessageSessionID())
				services.AdoptNativeSession(services.MessageSessionID())
			} else {
				writer.Retire()
				writer.Retire()
			}
			assert.False(t, writer.PublishSessionInfo(map[string]interface{}{"expired": true}))
			sink := requireRootOutputSink(t, svc.Output, "goal-publication-owner")
			info := sink.sessionInfoSnapshot()
			assert.Contains(t, info, contracts.SessionInfoKeyRateLimits)
			assert.Equal(t, !nativeReplacement, info[contracts.SessionInfoKeyContextUsage] != nil)
			assert.Equal(t, !nativeReplacement, info[contracts.SessionInfoKeyTotalCostUsd] != nil)
		})
	}
}
