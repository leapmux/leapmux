package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturedNativeChildStateRetainsItsOriginalRootSession(t *testing.T) {
	t.Parallel()
	for _, knownChildSession := range []bool{false, true} {
		for _, lateFactory := range []bool{false, true} {
			for _, replacement := range []string{"ordinary turn", "different session", "fresh same ID", "A B A", "root replacement"} {
				t.Run(fmt.Sprintf("%s/known_child=%t/late_factory=%t", replacement, knownChildSession, lateFactory), func(t *testing.T) {
					t.Parallel()
					h, queries, rootServices, _ := newCapturedPlanTest(t)
					childID, err := rootServices.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "captured-child", Title: "Child"})
					require.NoError(t, err)
					child := rootServices.ChildSink(childID)
					if knownChildSession {
						child.UpdateSessionID("child-native")
					}
					captured := agent.CaptureTranscript(child, agent.MessageContent{}, agent.SpanInfo{})
					var writer agent.CapturedPlanWriter
					if !lateFactory {
						writer, err = child.PlanWriterFor(captured)
						require.NoError(t, err)
					}
					switch replacement {
					case "ordinary turn":
						rootServices.SetTurnState(agent.TurnState{Active: true}, 1)
						rootServices.SetTurnState(agent.TurnState{}, 2)
					case "different session":
						rootServices.AdoptNativeSession("replacement")
					case "fresh same ID":
						rootServices.AdoptNativeSession("native")
					case "A B A":
						rootServices.UpdateSessionID("replacement")
						rootServices.UpdateSessionID("native")
					case "root replacement":
						_, err = h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
						require.NoError(t, err)
					}
					if lateFactory {
						writer, err = child.PlanWriterFor(captured)
						if replacement != "ordinary turn" {
							require.ErrorIs(t, err, errPlanWriterExpired)
							return
						}
						require.NoError(t, err)
					}
					err = writer.UpdatePlan([]byte("# Child plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Child plan")
					stored, readErr := queries.GetAgentByID(t.Context(), childID)
					require.NoError(t, readErr)
					if replacement == "ordinary turn" {
						require.NoError(t, err)
						data, readErr := os.ReadFile(stored.PlanFilePath)
						require.NoError(t, readErr)
						assert.Equal(t, "# Child plan", string(data))
					} else {
						require.ErrorIs(t, err, errPlanWriterExpired)
						assert.Empty(t, stored.PlanFilePath, "a stale child cannot write state after its root native session changes")
						assert.Empty(t, readAllNotifications(t, queries, childID))
					}
				})
			}
		}
	}
}

func TestCapturedIndependentChildTranscriptSurvivesRootNativeAdoption(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"different session", "fresh same ID", "A B A"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			h, _, rootServices, _ := newCapturedPlanTest(t)
			childID, err := rootServices.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "independent-child", Title: "Child"})
			require.NoError(t, err)
			child := rootServices.ChildSink(childID)
			child.UpdateSessionID("independent-native")
			child.SetTurnState(agent.TurnState{Active: true}, 1)
			raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "The independent child continues.", "")
			require.NoError(t, err)
			captured := agent.CaptureTranscript(child, agent.MessageContent{Original: raw}, agent.SpanInfo{})
			switch replacement {
			case "different session":
				rootServices.AdoptNativeSession("replacement")
			case "fresh same ID":
				rootServices.AdoptNativeSession("native")
			case "A B A":
				rootServices.UpdateSessionID("replacement")
				rootServices.UpdateSessionID("native")
			}
			assert.True(t, captured.FrozenContent().Publication.Owner().IsCurrent())
			assert.True(t, captured.FrozenContent().Publication.Owner().IsSessionCurrent())
			captured.FrozenContent().Publication.ReportProgress(agent.NativeTokenProgress("independent-model", 41))
			physical := h.sinkForAgent(childID)
			require.NotNil(t, physical)
			assert.Equal(t, int64(41), physical.progress.snapshotInfo()[contracts.SessionInfoKeyThinkingTokens])
			require.NoError(t, captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
		})
	}
}
