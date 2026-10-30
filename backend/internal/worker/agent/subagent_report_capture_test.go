package agent_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reportCaptureRoutes struct {
	agent.ProviderServices
	target    string
	children  map[string]*reportCaptureChild
	lookupErr error
	missing   bool
}

func (routes *reportCaptureRoutes) LookupBackgroundTask(string) (string, bgtask.Status, bool, error) {
	return routes.target, bgtask.StatusRunning, !routes.missing, routes.lookupErr
}
func (routes *reportCaptureRoutes) ChildSink(id string) agent.ProviderServices {
	if child := routes.children[id]; child != nil {
		return child
	}
	return nil
}

type reportCaptureChild struct {
	agent.ProviderServices
	captures      int
	notifications int
}

func (child *reportCaptureChild) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	child.captures++
	return child.ProviderServices.CaptureMessage(content, span)
}
func (child *reportCaptureChild) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	child.notifications++
	return child.ProviderServices.PersistNotification(source, content)
}

func TestCaptureChildSubagentReportRetainsItsOuterWriterAndNativeIdentity(t *testing.T) {
	t.Parallel()
	oldSink, newSink := &agenttest.Sink{}, &agenttest.Sink{}
	oldSink.UpdateSessionID("old")
	newSink.UpdateSessionID("new")
	oldChild, newChild := &reportCaptureChild{ProviderServices: agent.NewProviderServices(oldSink)}, &reportCaptureChild{ProviderServices: agent.NewProviderServices(newSink)}
	routes := &reportCaptureRoutes{ProviderServices: agent.NewProviderServices(&agenttest.Sink{}), target: "old", children: map[string]*reportCaptureChild{"old": oldChild, "new": newChild}}
	identity := " report-1 "
	captured, present, err := agent.CaptureChildSubagentReport(routes, agent.ChildSubagentReportWrite{RowKey: " row ", Write: agent.SubagentReportWrite{ReportID: identity, Report: agent.SubagentReport{Text: "Native report"}}})
	require.NoError(t, err)
	require.True(t, present)
	assert.Equal(t, 1, oldChild.captures)
	assert.Equal(t, fmt.Sprintf("subagent-report:%x", sha256.Sum256([]byte(identity))), captured.FrozenContent().IdempotencyKey)
	routes.target = "new"
	oldSink.AdoptNativeSession("replacement")
	_, err = captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
	require.NoError(t, err)
	assert.Equal(t, 1, oldChild.notifications)
	assert.Zero(t, newChild.notifications)
	rows := oldSink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, "old", rows[0].AgentSessionID)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rows[0].Content, &payload))
	assert.Equal(t, "Native report", payload["text"])
	assert.Empty(t, newSink.PersistedNotifications())
}

func TestCaptureChildSubagentReportRefusesMissingDestinations(t *testing.T) {
	t.Parallel()
	routes := &reportCaptureRoutes{ProviderServices: agent.NewProviderServices(&agenttest.Sink{}), target: "missing"}
	write := agent.ChildSubagentReportWrite{RowKey: "row", Write: agent.SubagentReportWrite{ReportID: "report", Report: agent.SubagentReport{Text: "Native report"}}}
	_, present, err := agent.CaptureChildSubagentReport(nil, write)
	require.Error(t, err)
	assert.False(t, present)
	_, present, err = agent.CaptureChildSubagentReport(routes, write)
	require.Error(t, err)
	assert.False(t, present)
	routes.missing = true
	_, present, err = agent.CaptureChildSubagentReport(routes, write)
	require.Error(t, err)
	assert.False(t, present)
	failure := errors.New("registry unavailable")
	routes.lookupErr = failure
	_, present, err = agent.CaptureChildSubagentReport(routes, write)
	require.ErrorIs(t, err, failure)
	assert.False(t, present)
}
