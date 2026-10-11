package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reentrantNotificationProvider struct {
	agent.Provider
	stage   string
	armed   atomic.Bool
	fired   atomic.Bool
	reenter func()
}

func (provider *reentrantNotificationProvider) enter(stage string) {
	if provider.stage == stage && provider.armed.Load() && provider.fired.CompareAndSwap(false, true) {
		provider.reenter()
	}
}

func (provider *reentrantNotificationProvider) Classify(json.RawMessage) agent.NotificationClassification {
	provider.enter("classify")
	return agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: "test:notice"}
}

func (provider *reentrantNotificationProvider) Merge(_ agent.NotificationClassification, _, next json.RawMessage) (json.RawMessage, error) {
	provider.enter("merge")
	return next, nil
}

func TestNotificationProviderPreparationPermitsReentry(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"classify", "merge"} {
		for _, operation := range []string{"publisher replacement", "notification append"} {
			t.Run(stage+" "+operation, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
				provider := &reentrantNotificationProvider{Provider: testRegistry.Plugin(providerID), stage: stage}
				svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
				const ownerID = "notification-reentry-owner"
				require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
				sink := svc.Output.NewSink(ownerID, providerID)
				sink.UpdateSessionID("native")
				_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(`{"type":"system","value":"first"}`), IdempotencyKey: "first"})
				require.NoError(t, err)
				nestedFinished := make(chan error, 1)
				callbackResult := make(chan error, 1)
				provider.reenter = func() {
					go func() {
						if operation == "publisher replacement" {
							replacement := svc.Output.NewSink(ownerID, providerID)
							replacement.SetTurnState(agent.TurnState{Active: true}, 1)
							nestedFinished <- nil
							return
						}
						_, nestedErr := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
							agent.MessageContent{Original: []byte(`{"type":"system","value":"nested"}`), IdempotencyKey: "nested"})
						nestedFinished <- nestedErr
					}()
					select {
					case nestedErr := <-nestedFinished:
						callbackResult <- nestedErr
						nestedFinished <- nestedErr
					case <-time.After(30 * time.Second):
						callbackResult <- errors.New("notification preparation holds a lock across provider reentry")
					}
				}
				provider.armed.Store(true)
				_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(`{"type":"system","value":"outer"}`), IdempotencyKey: "outer"})
				require.NoError(t, err)
				select {
				case nestedErr := <-nestedFinished:
					require.NoError(t, nestedErr)
				case <-ctx.Done():
					t.Fatal("the nested operation did not finish after provider preparation returned")
				}
				assert.NoError(t, <-callbackResult)
				rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
				require.NoError(t, err)
				require.Len(t, rows, 1)
				entries := readStoredNotificationEntries(t, rows[0])
				require.NotEmpty(t, entries)
				assert.Equal(t, "first", entries[0].IdempotencyKey)
				assertNotificationEntryIdentity(t, agent.MessageContent{Original: []byte(`{"type":"system","value":"first"}`), IdempotencyKey: "first"}, rows[0], entries[0])
				if operation == "notification append" {
					require.Len(t, entries, 3)
					assert.Equal(t, "nested", entries[1].IdempotencyKey)
					assert.Equal(t, "outer", entries[2].IdempotencyKey)
					assertNotificationEntryIdentity(t, agent.MessageContent{Original: []byte(`{"type":"system","value":"nested"}`), IdempotencyKey: "nested"}, rows[0], entries[1])
					assertNotificationEntryIdentity(t, agent.MessageContent{Original: []byte(`{"type":"system","value":"outer"}`), IdempotencyKey: "outer"}, rows[0], entries[2])
				} else {
					require.Len(t, entries, 2)
					assert.True(t, rows[0].TranscriptOnly)
					assertNotificationEntryIdentity(t, agent.MessageContent{Original: []byte(`{"type":"system","value":"outer"}`), IdempotencyKey: "outer"}, rows[0], entries[1])
				}
			})
		}
	}
}
