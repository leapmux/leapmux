package agenttest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingRegistryNativeKeysDoNotAliasDerivedKeys(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	first := strings.Repeat("x", bgtask.RowKeyByteLimit+1)
	second := fmt.Sprint(bgtask.NormalizeRowKey(first))
	require.NoError(t, bgtask.ValidateRowKey(second))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: first, Kind: bgtask.KindShell, Title: "First task", Status: bgtask.StatusRunning}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: second, Kind: bgtask.KindShell, Title: "Second task", Status: bgtask.StatusRunning}))
	rows := sink.BackgroundTasks()
	assert.Len(t, rows, 2)
	if len(rows) == 2 {
		assert.NotEqual(t, rows[0].RowKey, rows[1].RowKey)
		assert.ElementsMatch(t, []string{"First task", "Second task"}, []string{rows[0].Title, rows[1].Title})
	}
}

func TestRecordingChildSubagentReportKeepsTheExactRegistryKey(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	first, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: " child-key ", Title: "Spaced child"})
	require.NoError(t, err)
	second, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "child-key", Title: "Plain child"})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	write := agent.ChildSubagentReportWrite{
		RowKey: " child-key ",
		Write:  agent.SubagentReportWrite{ReportID: "spaced-report", Report: agent.SubagentReport{Text: "Keep the report with the spaced native key."}},
	}
	stored, err := sink.PersistChildSubagentReport(write)
	require.NoError(t, err)
	assert.True(t, stored)
	firstSink, secondSink := sink.Child(first), sink.Child(second)
	require.NotNil(t, firstSink)
	require.NotNil(t, secondSink)
	reports := firstSink.LeapMuxNotifications()
	assert.Len(t, reports, 1)
	if len(reports) == 1 {
		assert.Equal(t, write.Write.Report.Text, reports[0]["text"])
	}
	assert.Empty(t, secondSink.LeapMuxNotifications(), "the plain child must not receive the spaced child's report")
	write.RowKey = ""
	write.Write.ReportID = "empty-key-report"
	stored, err = sink.PersistChildSubagentReport(write)
	assert.ErrorContains(t, err, "no row key")
	assert.False(t, stored)
}
