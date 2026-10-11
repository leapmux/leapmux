package service

import (
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogFinalAdmissionUsesTheRowAfterAConcurrentWriter(t *testing.T) {
	var process *catalogObservationAgent
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
		process = &catalogObservationAgent{Agent: underlying}
		return process
	})
	svc.Output.WaitActivityRefreshes()
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	process.observeNext(func() {
		close(entered)
		<-release
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		services.BroadcastStatusActive("original-native-session")
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		resume()
		<-done
		t.Fatal("the actual provider sample did not reach its controlled hold")
	}
	transaction, err := svc.DB.BeginTx(t.Context(), nil)
	if err != nil {
		resume()
		<-done
		require.NoError(t, err)
		return
	}
	defer func() { _ = transaction.Rollback() }()
	queries := db.New(transaction)
	row, err := queries.GetAgentByID(t.Context(), "agent-1")
	if err != nil {
		_ = transaction.Rollback()
		resume()
		<-done
		require.NoError(t, err)
		return
	}
	options := parseOptions(row.Options)
	options[agent.OptionIDModel] = fixtureModelA
	options[agent.OptionIDEffort] = "low"
	options["transaction_option"] = "preserved"
	err = queries.SetAgentOptions(t.Context(), db.SetAgentOptionsParams{ID: row.ID, Options: marshalOptions(options)})
	if err != nil {
		_ = transaction.Rollback()
		resume()
		<-done
		require.NoError(t, err)
		return
	}
	result := process.UpdateSettings(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
	assert.True(t, result.AppliedLive)
	resume()
	err = transaction.Commit()
	if err != nil {
		_ = transaction.Rollback()
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the catalog operation did not complete after the actual concurrent writer committed")
	}
	require.NoError(t, err)
	stored := mustGetAgent(t, svc)
	assert.Equal(t, options, parseOptions(stored.Options))
	assert.NotContains(t, effortLevels(parseOptionGroups(stored.OptionGroups)), "max",
		"the final catalog decision must compare the actual committed writer row before it stores the old sample")
	changes := agentStatusChanges(t, writer, "agent-1")[beforeEvents:]
	require.Len(t, changes, 1)
	assert.Equal(t, fixtureModelA, optionids.CurrentValue(changes[0].GetOptionGroups(), agent.OptionIDModel))
	assert.Equal(t, "low", optionids.CurrentValue(changes[0].GetOptionGroups(), agent.OptionIDEffort))
	assert.NotContains(t, effortLevels(changes[0].GetOptionGroups()), "max")
}
