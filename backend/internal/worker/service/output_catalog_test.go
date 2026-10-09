package service

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type catalogObservationAgent struct {
	agent.Agent
	mu      sync.Mutex
	observe func()
	samples atomic.Int64
}

func (process *catalogObservationAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	process.samples.Add(1)
	groups := process.Agent.OptionGroups()
	process.mu.Lock()
	observe := process.observe
	process.observe = nil
	process.mu.Unlock()
	if observe != nil {
		observe()
	}
	return groups
}

func (process *catalogObservationAgent) observeNext(observe func()) {
	process.mu.Lock()
	process.observe = observe
	process.mu.Unlock()
}

func catalogCallbackLocksFree(sink *agentOutputSink) bool {
	catalogFree := sink.catalogMu.TryLock()
	if catalogFree {
		sink.catalogMu.Unlock()
	}
	rootFree := sink.h.rootSinkMu.TryLock()
	if rootFree {
		sink.h.rootSinkMu.Unlock()
	}
	return catalogFree && rootFree
}

func catalogFailureForAgent(record slog.Record, message string, level slog.Level) (error, bool) {
	if record.Message != message || record.Level != level {
		return nil, false
	}
	var agentID string
	var failure error
	record.Attrs(func(attribute slog.Attr) bool {
		switch attribute.Key {
		case "agent_id":
			agentID = attribute.Value.String()
		case "error":
			failure, _ = attribute.Value.Any().(error)
		}
		return true
	})
	return failure, agentID == "agent-1"
}

func liveCatalogCallbackFixture(t *testing.T, storedModel string, wrap ...func(agent.Agent) agent.Agent) (*Service, *testResponseWriter, agent.ProviderServices) {
	t.Helper()
	svc, writer, services := liveSwitchFixture(t, storedModel, wrap...)
	services.UpdateSessionID("original-native-session")
	svc.Output.WaitActivityRefreshes()
	return svc, writer, services
}

func TestCatalogProviderSampleCanPublishTheSameSink(t *testing.T) {
	for _, operation := range []string{"catalog confirmation", "active status"} {
		t.Run(operation, func(t *testing.T) {
			var process *catalogObservationAgent
			svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
				process = &catalogObservationAgent{Agent: underlying}
				return process
			})
			sink := requireRootOutputSink(t, svc.Output, "agent-1")
			svc.Output.WaitActivityRefreshes()
			before := len(agentStatusChanges(t, writer, "agent-1"))
			var callbacks int
			var mutexesFree bool
			process.observeNext(func() {
				mutexesFree = catalogCallbackLocksFree(sink)
				if !mutexesFree {
					return
				}
				callbacks++
				if operation == "catalog confirmation" {
					services.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: fixtureModelB, agent.OptionIDEffort: "low"})
				} else {
					services.BroadcastStatusActive("original-native-session")
				}
			})
			if operation == "catalog confirmation" {
				services.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: fixtureModelB, agent.OptionIDEffort: "low"})
			} else {
				services.BroadcastStatusActive("original-native-session")
			}
			assert.True(t, mutexesFree, "the actual provider sample must release both service mutexes")
			assert.Equal(t, 1, callbacks, "the provider callback must publish the same sink synchronously")
			changes := agentStatusChanges(t, writer, "agent-1")[before:]
			expected := 1
			status := leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED
			if operation == "active status" {
				expected = 2
				status = leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
			}
			assert.Len(t, changes, expected)
			for _, change := range changes {
				assert.Equal(t, status, change.GetStatus())
				assert.Equal(t, fixtureModelB, optionids.CurrentValue(change.GetOptionGroups(), agent.OptionIDModel))
				assert.Contains(t, effortLevels(change.GetOptionGroups()), "max")
			}
			assert.Contains(t, effortLevels(parseOptionGroups(mustGetAgent(t, svc).OptionGroups)), "max")
		})
	}
}

func TestCatalogCapabilitiesCanPublishTheSameSink(t *testing.T) {
	for _, capability := range []string{"steering", "preemption"} {
		t.Run(capability, func(t *testing.T) {
			svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
			sink := requireRootOutputSink(t, svc.Output, "agent-1")
			svc.Output.WaitActivityRefreshes()
			original := svc.Output.supportsSteering
			if capability == "preemption" {
				original = svc.Output.supportsPreemption
			}
			expected := original("agent-1")
			before := len(agentStatusChanges(t, writer, "agent-1"))
			var entered bool
			var callbacks int
			var mutexesFree bool
			callback := func(agentID string) bool {
				if !entered {
					entered = true
					mutexesFree = catalogCallbackLocksFree(sink)
					if mutexesFree {
						callbacks++
						services.BroadcastStatusActive("original-native-session")
					}
				}
				return original(agentID)
			}
			if capability == "steering" {
				svc.Output.SetSupportsSteeringFunc(callback)
			} else {
				svc.Output.SetSupportsPreemptionFunc(callback)
			}
			services.BroadcastStatusActive("original-native-session")
			assert.True(t, mutexesFree, "the configured capability reader must release both service mutexes")
			assert.Equal(t, 1, callbacks)
			changes := agentStatusChanges(t, writer, "agent-1")[before:]
			assert.Len(t, changes, 2)
			for _, change := range changes {
				if capability == "steering" {
					assert.Equal(t, expected, change.GetSupportsSteering())
				} else {
					assert.Equal(t, expected, change.GetSupportsPreemption())
				}
			}
		})
	}
}

func TestCatalogCloseReaderCanPublishTheSameSink(t *testing.T) {
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	finish := holdCloseBeforeTeardown(t, svc, "agent-1")
	svc.Output.WaitActivityRefreshes()
	original := svc.Output.agentClosing
	before := len(agentStatusChanges(t, writer, "agent-1"))
	var entered bool
	var callbacks int
	var mutexesFree bool
	svc.Output.SetAgentClosingFunc(func(agentID string) bool {
		if !entered {
			entered = true
			mutexesFree = catalogCallbackLocksFree(sink)
			if mutexesFree {
				callbacks++
				services.BroadcastStatusActive("original-native-session")
			}
		}
		return original(agentID)
	})
	services.BroadcastStatusActive("original-native-session")
	assert.True(t, mutexesFree, "the configured close reader must release both service mutexes")
	assert.Equal(t, 1, callbacks)
	changes := agentStatusChanges(t, writer, "agent-1")[before:]
	assert.Len(t, changes, 2)
	for _, change := range changes {
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, change.GetStatus())
	}
	finish()
	require.True(t, mustGetAgent(t, svc).ClosedAt.Valid)
}

func TestSettingsStartupReaderCanReplaceTheRoot(t *testing.T) {
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	svc.Output.WaitActivityRefreshes()
	before := mustGetAgent(t, svc)
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	original := svc.Output.agentStarting
	var mutexesFree bool
	var replacement *agentOutputSink
	svc.Output.SetAgentStartingFunc(func(agentID string) bool {
		mutexesFree = catalogCallbackLocksFree(sink)
		if mutexesFree {
			svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			replacement = requireRootOutputSink(t, svc.Output, agentID)
		}
		return original(agentID)
	})
	services.PersistSettingsRefresh(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, mutexesFree, "the configured startup reader must release both service mutexes")
	if assert.NotNil(t, replacement, "the real configured reader must replace the root synchronously") {
		assert.NotSame(t, sink, replacement)
		assert.Same(t, replacement, svc.Output.sinkForAgent("agent-1"))
	}
	assert.Equal(t, before, mustGetAgent(t, svc), "the retired preparation must change no replacement row")
	assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents)
}

func TestCatalogNestedNoOpPreservesTheLatestSample(t *testing.T) {
	var process *catalogObservationAgent
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
		process = &catalogObservationAgent{Agent: underlying}
		return process
	})
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	require.NoError(t, svc.Queries.SetAgentOptions(t.Context(), db.SetAgentOptionsParams{
		ID: "agent-1", Options: marshalOptions(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"}),
	}))
	svc.Output.WaitActivityRefreshes()
	before := mustGetAgent(t, svc)
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	var callbacks int
	var mutexesFree bool
	process.observeNext(func() {
		mutexesFree = catalogCallbackLocksFree(sink)
		if !mutexesFree {
			return
		}
		callbacks++
		result := process.UpdateSettings(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
		assert.True(t, result.AppliedLive)
		services.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
	})
	services.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
	assert.True(t, mutexesFree)
	assert.Equal(t, 1, callbacks)
	assert.Equal(t, before, mustGetAgent(t, svc), "the nested native A sample must keep the existing A row")
	assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents, "an older B sample must not create a status event after the nested no-op")
}

func TestCatalogConcurrentSamplesPreserveAdmissionOrder(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var process *catalogObservationAgent
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
		process = &catalogObservationAgent{Agent: underlying}
		return process
	})
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	before := len(agentStatusChanges(t, writer, "agent-1"))
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	var mutexesFree bool
	process.observeNext(func() {
		mutexesFree = catalogCallbackLocksFree(sink)
		close(entered)
		<-release
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		services.BroadcastStatusActive("original-native-session")
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		resume()
		<-done
		t.Fatal("the first catalog sample did not reach its controlled hold")
	}
	if !mutexesFree {
		resume()
		<-done
		t.Fatal("the first provider sample retained a service mutex")
	}
	result := process.UpdateSettings(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
	assert.True(t, result.AppliedLive)
	// This actual refresh must finish while the earlier provider sample waits.
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		services.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
	}()
	completedWhileHeld := false
	select {
	case <-secondDone:
		completedWhileHeld = true
	case <-ctx.Done():
	}
	resume()
	<-secondDone
	<-done
	require.True(t, completedWhileHeld, "the later real refresh must complete before the earlier provider observation resumes")
	row := mustGetAgent(t, svc)
	assert.Equal(t, fixtureModelA, parseOptions(row.Options)[agent.OptionIDModel])
	assert.NotContains(t, effortLevels(parseOptionGroups(row.OptionGroups)), "max")
	changes := agentStatusChanges(t, writer, "agent-1")[before:]
	assert.Len(t, changes, 2)
	for _, change := range changes {
		assert.Equal(t, fixtureModelA, optionids.CurrentValue(change.GetOptionGroups(), agent.OptionIDModel))
		assert.NotContains(t, effortLevels(change.GetOptionGroups()), "max", "the older sample must not overwrite the accepted newer catalog")
	}
}

func TestCatalogFailureLogsReleaseLocksBeforeCallbacks(t *testing.T) {
	// slog.SetDefault changes process state, so these cases run without t.Parallel.
	for _, test := range []struct {
		name    string
		message string
		level   slog.Level
	}{
		{name: "catalog confirmation read", message: "The Worker failed to read the agent for the catalog broadcast.", level: slog.LevelError},
		{name: "live catalog read", message: "failed to fetch agent for catalog persist", level: slog.LevelError},
		{name: "active status read", message: "failed to fetch agent for status broadcast", level: slog.LevelError},
		{name: "catalog marshal", message: "skipping discovered option-group catalog persist; marshal failed", level: slog.LevelWarn},
		{name: "catalog write", message: "failed to persist discovered option-group catalog", level: slog.LevelWarn},
	} {
		t.Run(test.name, func(t *testing.T) {
			var process *catalogObservationAgent
			svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
				process = &catalogObservationAgent{Agent: underlying}
				return process
			})
			sink := requireRootOutputSink(t, svc.Output, "agent-1")
			before := mustGetAgent(t, svc)
			beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
			live := svc.Agents.LiveOptionGroups("agent-1", before.AgentProvider)
			require.NotEmpty(t, live)
			if strings.HasSuffix(test.name, "read") {
				svc.Output.queries = db.New(&failedSessionComparisonStore{DBTX: svc.DB, agentID: "agent-1"})
			}
			if test.name == "catalog marshal" {
				live = append([]*leapmuxv1.AvailableOptionGroup(nil), live...)
				live[0] = proto.Clone(live[0]).(*leapmuxv1.AvailableOptionGroup)
				live[0].Label = "\xff"
			}
			if test.name == "catalog write" {
				_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_logged_catalog BEFORE UPDATE OF option_groups ON agents
WHEN NEW.id = 'agent-1' BEGIN SELECT RAISE(ABORT, 'catalog write refused'); END`)
				require.NoError(t, err)
			}
			var logs, callbacks int
			var mutexesFree bool
			var loggedError, repairError error
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				failure, belongs := catalogFailureForAgent(record, test.message, test.level)
				if !belongs {
					return
				}
				logs++
				loggedError = failure
				mutexesFree = catalogCallbackLocksFree(sink)
				if !mutexesFree {
					return
				}
				assert.Equal(t, before.OptionGroups, mustGetAgent(t, svc).OptionGroups, "the failed original mutation must leave storage unchanged before the callback")
				if test.name == "catalog write" {
					_, repairError = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_logged_catalog")
					if repairError != nil {
						return
					}
				}
				callbacks++
				services.BroadcastStatusActive("original-native-session")
			}}))
			defer slog.SetDefault(previousLogger)
			beforeSamples := process.samples.Load()
			switch test.name {
			case "catalog confirmation read":
				sink.broadcastChangedCatalog()
			case "active status read":
				services.BroadcastStatusActive("original-native-session")
			default:
				sink.persistLiveCatalog(live)
			}
			assert.Equal(t, 1, logs)
			assert.Equal(t, int64(1), process.samples.Load()-beforeSamples,
				"a failed read, marshal, or write must not repeat the actual provider sample")
			assert.Error(t, loggedError)
			switch test.name {
			case "catalog confirmation read", "live catalog read", "active status read":
				assert.ErrorContains(t, loggedError, "missing_session_comparison_column")
			case "catalog marshal":
				assert.ErrorContains(t, loggedError, "UTF-8")
			case "catalog write":
				assert.ErrorContains(t, loggedError, "catalog write refused")
			}
			assert.NoError(t, repairError)
			assert.True(t, mutexesFree, "the original failure logger must release both service mutexes")
			assert.Equal(t, 1, callbacks, "the handler must execute the real nested publication")
			assert.Contains(t, effortLevels(parseOptionGroups(mustGetAgent(t, svc).OptionGroups)), "max")
			changes := agentStatusChanges(t, writer, "agent-1")[beforeEvents:]
			assert.Len(t, changes, 1)
			if assert.NotEmpty(t, changes) {
				assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, changes[0].GetStatus())
				assert.Equal(t, "original-native-session", changes[0].GetAgentSessionId())
			}
		})
	}
}

func TestSettingsFailureLogsReleaseLocksBeforeCallbacks(t *testing.T) {
	for _, operation := range []string{"settings read", "settings write"} {
		t.Run(operation, func(t *testing.T) {
			svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
			sink := requireRootOutputSink(t, svc.Output, "agent-1")
			before := mustGetAgent(t, svc)
			beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
			message := "failed to fetch agent for settings broadcast"
			if operation == "settings read" {
				svc.Output.queries = db.New(&failedSessionComparisonStore{DBTX: svc.DB, agentID: "agent-1"})
			} else {
				message = "failed to persist refreshed settings"
				_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_logged_settings BEFORE UPDATE OF options ON agents
WHEN NEW.id = 'agent-1' BEGIN SELECT RAISE(ABORT, 'settings write refused'); END`)
				require.NoError(t, err)
			}
			var logs int
			var mutexesFree bool
			var loggedError error
			var replacement *agentOutputSink
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				failure, belongs := catalogFailureForAgent(record, message, slog.LevelError)
				if !belongs {
					return
				}
				logs++
				loggedError = failure
				mutexesFree = catalogCallbackLocksFree(sink)
				if mutexesFree {
					svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
					replacement = requireRootOutputSink(t, svc.Output, "agent-1")
				}
			}}))
			defer slog.SetDefault(previousLogger)
			services.PersistSettingsRefresh(map[string]string{agent.OptionIDEffort: "high"})
			assert.Equal(t, 1, logs)
			assert.Error(t, loggedError)
			if operation == "settings read" {
				assert.ErrorContains(t, loggedError, "missing_session_comparison_column")
			} else {
				assert.ErrorContains(t, loggedError, "settings write refused")
			}
			assert.True(t, mutexesFree)
			if assert.NotNil(t, replacement, "the actual handler must replace the root synchronously") {
				assert.NotSame(t, sink, replacement)
				assert.Equal(t, "original-native-session", replacement.currentMessageSessionID())
			}
			assert.Equal(t, before, mustGetAgent(t, svc), "the refused settings write must retain every original row field")
			assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents)
		})
	}
}

type contendingCatalogOptionsStore struct {
	db.DBTX
	agentID  string
	attempts atomic.Int64
}

func (store *contendingCatalogOptionsStore) ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error) {
	if strings.HasPrefix(query, "-- name: SetAgentOptionsIfUnchanged :execrows") && len(arguments) == 3 && arguments[1] == store.agentID {
		attempt := store.attempts.Add(1)
		var stored string
		if err := store.DBTX.QueryRowContext(ctx, "SELECT options FROM agents WHERE id = ?", store.agentID).Scan(&stored); err != nil {
			return nil, err
		}
		options := parseOptions(stored)
		options["concurrent_option"] = strconv.FormatInt(attempt, 10)
		if _, err := store.DBTX.ExecContext(ctx, "UPDATE agents SET options = ? WHERE id = ?", marshalOptions(options), store.agentID); err != nil {
			return nil, err
		}
	}
	return store.DBTX.ExecContext(ctx, query, arguments...)
}

func TestSettingsCASExhaustionLogsAfterAdmission(t *testing.T) {
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	store := &contendingCatalogOptionsStore{DBTX: svc.DB, agentID: "agent-1"}
	svc.Output.queries = db.New(store)
	var logs int
	var attempts int64
	var mutexesFree bool
	var replacement *agentOutputSink
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
		_, belongs := catalogFailureForAgent(record, "options CAS exhausted; applied final last-writer-wins merge", slog.LevelWarn)
		if !belongs {
			return
		}
		logs++
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "attempts" {
				attempts = attribute.Value.Int64()
			}
			return true
		})
		assert.Equal(t, OptionMap{
			agent.OptionIDModel: fixtureModelB, agent.OptionIDEffort: "high", "concurrent_option": "8",
		}, parseOptions(mustGetAgent(t, svc).Options), "the real final merge must commit before its warning")
		mutexesFree = catalogCallbackLocksFree(sink)
		if mutexesFree {
			svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			replacement = requireRootOutputSink(t, svc.Output, "agent-1")
		}
	}}))
	defer slog.SetDefault(previousLogger)
	services.PersistSettingsRefresh(map[string]string{agent.OptionIDEffort: "high"})
	assert.Equal(t, 1, logs)
	assert.Equal(t, int64(8), attempts)
	assert.Equal(t, int64(8), store.attempts.Load(), "eight real competing writes must defeat eight real SQL comparisons")
	assert.True(t, mutexesFree)
	if assert.NotNil(t, replacement, "the committed warning's handler must replace the root synchronously") {
		assert.NotSame(t, sink, replacement)
		assert.Equal(t, "original-native-session", replacement.currentMessageSessionID())
	}
	assert.Equal(t, OptionMap{
		agent.OptionIDModel: fixtureModelB, agent.OptionIDEffort: "high", "concurrent_option": "8",
	}, parseOptions(mustGetAgent(t, svc).Options))
	assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents, "the retired source must not publish after the actual logger replacement")
}

func TestCatalogSourceReplacementDuringProviderSample(t *testing.T) {
	var process *catalogObservationAgent
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
		process = &catalogObservationAgent{Agent: underlying}
		return process
	})
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	before := mustGetAgent(t, svc)
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	var replacement *agentOutputSink
	var mutexesFree bool
	process.observeNext(func() {
		mutexesFree = catalogCallbackLocksFree(sink)
		if !mutexesFree {
			return
		}
		svc.Output.NewSink("agent-1", before.AgentProvider)
		replacement = requireRootOutputSink(t, svc.Output, "agent-1")
	})
	services.BroadcastStatusActive("original-native-session")
	assert.True(t, mutexesFree, "the actual provider callback must run without either service mutex")
	if assert.NotNil(t, replacement, "the callback must replace the actual registered root") {
		assert.NotSame(t, sink, replacement)
	}
	assert.Equal(t, before, mustGetAgent(t, svc), "an obsolete source must change no stored row field")
	assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents,
		"an obsolete sample must publish no status after its actual root replacement")
}

func TestCatalogPreparationUsesTheChangedRow(t *testing.T) {
	var process *catalogObservationAgent
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA, func(underlying agent.Agent) agent.Agent {
		process = &catalogObservationAgent{Agent: underlying}
		return process
	})
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	before := mustGetAgent(t, svc)
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	var mutexesFree bool
	process.observeNext(func() {
		mutexesFree = catalogCallbackLocksFree(sink)
		if !mutexesFree {
			return
		}
		result := process.UpdateSettings(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"})
		require.True(t, result.AppliedLive)
		options := parseOptions(before.Options)
		options[agent.OptionIDModel] = fixtureModelA
		options[agent.OptionIDEffort] = "low"
		options["unrelated"] = "preserved"
		require.NoError(t, svc.Queries.SetAgentOptions(t.Context(), db.SetAgentOptionsParams{
			ID: before.ID, Options: marshalOptions(options),
		}))
	})
	services.BroadcastStatusActive("original-native-session")
	assert.True(t, mutexesFree)
	row := mustGetAgent(t, svc)
	assert.Equal(t, "preserved", parseOptions(row.Options)["unrelated"])
	assert.Equal(t, "low", parseOptions(row.Options)[agent.OptionIDEffort])
	assert.NotContains(t, effortLevels(parseOptionGroups(row.OptionGroups)), "max",
		"the retained old model sample must not overwrite the changed row's current native catalog")
	changes := agentStatusChanges(t, writer, "agent-1")[beforeEvents:]
	require.Len(t, changes, 1)
	assert.Equal(t, fixtureModelA, optionids.CurrentValue(changes[0].GetOptionGroups(), agent.OptionIDModel))
	assert.Equal(t, "low", optionids.CurrentValue(changes[0].GetOptionGroups(), agent.OptionIDEffort))
	assert.NotContains(t, effortLevels(changes[0].GetOptionGroups()), "max")
}
