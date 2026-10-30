package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCapturedPlanTest(t *testing.T) (*OutputHandler, *db.Queries, agent.ProviderServices, agent.CapturedPlanWriter) {
	t.Helper()
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "plan-owner", "Agent")
	services, err := h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)
	services.UpdateSessionID("native")
	captured := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	writer, err := services.PlanWriterFor(captured)
	require.NoError(t, err)
	t.Cleanup(h.CancelBackgroundCtx)
	return h, queries, services, writer
}

func TestCapturedPlanWriter_NativeLifetimeAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"ordinary turns", "same ID update", "different session", "fresh same ID", "A B A", "root replacement"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			h, queries, services, writer := newCapturedPlanTest(t)
			switch scenario {
			case "ordinary turns":
				services.SetTurnState(agent.TurnState{Active: true}, 1)
				services.SetTurnState(agent.TurnState{}, 2)
			case "same ID update":
				services.UpdateSessionID("native")
			case "different session":
				services.AdoptNativeSession("replacement")
			case "fresh same ID":
				services.AdoptNativeSession("native")
			case "A B A":
				services.UpdateSessionID("other")
				services.UpdateSessionID("native")
			case "root replacement":
				_, err := h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				require.NoError(t, err)
			}
			err := writer.UpdatePlan([]byte("# Captured plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Captured plan")
			row, readErr := queries.GetAgentByID(t.Context(), "plan-owner")
			require.NoError(t, readErr)
			if scenario == "ordinary turns" || scenario == "same ID update" {
				require.NoError(t, err)
				content, err := os.ReadFile(row.PlanFilePath)
				require.NoError(t, err)
				assert.Equal(t, "# Captured plan", string(content))
			} else {
				require.ErrorIs(t, err, errPlanWriterExpired)
				assert.Empty(t, row.PlanFilePath)
				assert.Empty(t, readAllNotifications(t, queries, "plan-owner"))
			}
		})
	}
}

func TestCapturedPlanWriter_RejectsForeignAndAbsentCaptures(t *testing.T) {
	t.Parallel()
	h, queries, services, _ := newCapturedPlanTest(t)
	_, err := services.PlanWriterFor(agent.CapturedTranscript{})
	require.Error(t, err)
	foreign := agent.CaptureTranscript(services, agent.MessageContent{AgentSessionID: "foreign"}, agent.SpanInfo{})
	_, err = services.PlanWriterFor(foreign)
	require.Error(t, err)
	createTestAgent(t, queries, "other-owner", "Other")
	other, err := h.NewRootSink("other-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)
	other.UpdateSessionID("native")
	_, err = other.PlanWriterFor(agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{}))
	require.Error(t, err)
}

func TestCapturedPlanWriter_ClockCallbackCanReplaceItsSource(t *testing.T) {
	t.Parallel()
	h, queries, services, writer := newCapturedPlanTest(t)
	now := h.now()
	h.now = func() time.Time { services.AdoptNativeSession("native"); return now }
	result := make(chan error, 1)
	go func() {
		result <- writer.UpdatePlan([]byte("# Old"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Old")
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, errPlanWriterExpired)
	case <-ctx.Done():
		t.Fatal("plan preparation held source authority through the clock callback")
	}
	row, err := queries.GetAgentByID(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Empty(t, row.PlanFilePath)
}

type planNotificationHook struct {
	agent.ProviderServices
	once sync.Once
	hook func()
}

func (services *planNotificationHook) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	services.once.Do(services.hook)
	return services.ProviderServices.PersistNotification(source, content)
}

func TestCapturedPlanWriter_ReentrantPublicationKeepsCommitOrder(t *testing.T) {
	t.Parallel()
	h, queries, services, _ := newCapturedPlanTest(t)
	hooked := &planNotificationHook{ProviderServices: services}
	var reentrantErr error
	hooked.hook = func() {
		services.AdoptNativeSession("replacement")
		next, err := services.PlanWriterFor(agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{}))
		if err == nil {
			err = next.UpdatePlan([]byte("# New"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "New")
		}
		reentrantErr = err
	}
	writer, err := services.PlanWriterFor(agent.CaptureTranscript(hooked, agent.MessageContent{}, agent.SpanInfo{}))
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		result <- writer.UpdatePlan([]byte("# Old"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Old")
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case err := <-result:
		require.NoError(t, errors.Join(err, reentrantErr))
	case <-ctx.Done():
		t.Fatal("plan publication held an operation or source lock")
	}
	row, err := queries.GetAgentByID(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Equal(t, "New", row.PlanTitle)
	content, err := os.ReadFile(row.PlanFilePath)
	require.NoError(t, err)
	assert.Equal(t, "# New", string(content))
	notices := readAllNotifications(t, queries, "plan-owner")
	require.Len(t, notices, 2)
	assert.Equal(t, "Old", notices[0]["plan_title"])
	assert.Equal(t, "New", notices[1]["plan_title"])
	assert.Empty(t, h.planStateFor("plan-owner").pending)
}

func TestCapturedPlanWriter_RetainsAnExplicitUnknownSessionFact(t *testing.T) {
	t.Parallel()
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "plan-owner", "Agent")
	services, err := h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, err)
	t.Cleanup(h.CancelBackgroundCtx)
	writer, err := services.PlanWriterFor(agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{}))
	require.NoError(t, err)
	require.NoError(t, writer.UpdatePlan([]byte("# Before native identity"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Before native identity"))
	services.UpdateSessionID("native")
	require.ErrorIs(t, writer.UpdatePlan([]byte("# Old observation"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Old observation"), errPlanWriterExpired)
	row, err := queries.GetAgentByID(t.Context(), "plan-owner")
	require.NoError(t, err)
	content, err := os.ReadFile(row.PlanFilePath)
	require.NoError(t, err)
	assert.Equal(t, "# Before native identity", string(content))
}

func TestCapturedPlanWriter_DoesNotReuseTheNativeRecordIdentity(t *testing.T) {
	t.Parallel()
	_, queries, services, _ := newCapturedPlanTest(t)
	captured := agent.CaptureTranscript(services, agent.MessageContent{Original: []byte(`{"native":"event"}`), IdempotencyKey: "native-key", Supplemental: []byte(`{"native":"extra"}`)}, agent.SpanInfo{})
	require.NoError(t, captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	writer, err := services.PlanWriterFor(captured)
	require.NoError(t, err)
	require.NoError(t, writer.UpdatePlan([]byte("# First plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "First plan"))
	require.NoError(t, writer.UpdatePlan([]byte("# Second plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Second plan"))
	notices := readAllNotifications(t, queries, "plan-owner")
	require.Len(t, notices, 1, "ordinary plan notifications retain the latest update")
	assert.Equal(t, "Second plan", notices[0]["plan_title"], "the native identity must not suppress the later plan update")
}

func TestRestorePlanFile_AttemptsRestorationAfterCleanupFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prior, snapshot, created := filepath.Join(dir, "prior.md"), filepath.Join(dir, "snapshot.md"), filepath.Join(dir, "new")
	require.NoError(t, os.WriteFile(snapshot, []byte("# Prior"), 0o600))
	require.NoError(t, os.Mkdir(created, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(created, "other"), []byte("keep"), 0o600))
	require.Error(t, restorePlanFile(prior, snapshot, created), "the nonempty replacement directory cannot be removed as a file")
	content, err := os.ReadFile(prior)
	require.NoError(t, err)
	assert.Equal(t, "# Prior", string(content))
	require.FileExists(t, filepath.Join(created, "other"), "cleanup must not delete unrelated directory contents")
}
