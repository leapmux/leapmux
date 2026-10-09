package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryNativeKeysDoNotAliasDerivedKeys(t *testing.T) {
	t.Parallel()
	_, sink, _, listRows := setupBgTaskTestWithService(t)
	first := strings.Repeat("x", bgtask.RowKeyByteLimit+1)
	second := fmt.Sprint(bgtask.NormalizeRowKey(first))
	require.NoError(t, bgtask.ValidateRowKey(second))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: first, Kind: bgtask.KindShell, Title: "First task", Status: bgtask.StatusRunning}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: second, Kind: bgtask.KindShell, Title: "Second task", Status: bgtask.StatusRunning}))
	rows := listRows()
	assert.Len(t, rows, 2)
	if len(rows) == 2 {
		assert.NotEqual(t, rows[0].RowKey, rows[1].RowKey)
		assert.ElementsMatch(t, []string{"First task", "Second task"}, []string{rows[0].Title, rows[1].Title})
	}
}

func TestChildSubagentReportKeepsTheExactRegistryKey(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "exact-report-owner")
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
	firstRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: first})
	require.NoError(t, err)
	secondRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: second})
	require.NoError(t, err)
	assert.Len(t, firstRows, 1)
	assert.Empty(t, secondRows, "the plain child must not receive the spaced child's report")
	write.RowKey = ""
	write.Write.ReportID = "empty-key-report"
	stored, err = sink.PersistChildSubagentReport(write)
	assert.ErrorContains(t, err, "no row key")
	assert.False(t, stored)
}
