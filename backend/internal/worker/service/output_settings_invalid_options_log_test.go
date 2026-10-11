package service

import (
	"io"
	"log/slog"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingsInvalidOptionsLogReleasesAdmissionBeforeReplacement(t *testing.T) {
	svc, writer, services := liveCatalogCallbackFixture(t, fixtureModelA)
	sink := requireRootOutputSink(t, svc.Output, "agent-1")
	require.NoError(t, svc.Queries.SetAgentOptions(t.Context(), db.SetAgentOptionsParams{
		ID: "agent-1", Options: "malformed actual stored options",
	}))
	svc.Output.WaitActivityRefreshes()
	beforeEvents := len(agentStatusChanges(t, writer, "agent-1"))
	var entered bool
	var mutexesFree bool
	var loggedError error
	var replacement *agentOutputSink
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(controlFailureLogObserver{
		Handler: slog.NewTextHandler(io.Discard, nil),
		observe: func(record slog.Record) {
			if entered || record.Message != "invalid agent options payload; using empty object" || record.Level != slog.LevelWarn {
				return
			}
			entered = true
			record.Attrs(func(attribute slog.Attr) bool {
				if attribute.Key == "error" {
					loggedError, _ = attribute.Value.Any().(error)
				}
				return true
			})
			mutexesFree = catalogCallbackLocksFree(sink)
			if mutexesFree {
				svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				replacement = requireRootOutputSink(t, svc.Output, "agent-1")
			}
		},
	}))
	defer slog.SetDefault(previousLogger)
	services.PersistSettingsRefresh(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, entered, "the actual malformed stored options must reach the original parser warning")
	assert.Error(t, loggedError)
	assert.True(t, mutexesFree, "the original parser warning must release catalog and root admission before its handler")
	if assert.NotNil(t, replacement, "the actual parser-warning handler must replace the root synchronously") {
		assert.NotSame(t, sink, replacement)
		assert.Same(t, replacement, svc.Output.sinkForAgent("agent-1"))
	}
	assert.Len(t, agentStatusChanges(t, writer, "agent-1"), beforeEvents,
		"the retired refresh must publish no catalog or status after actual logger replacement")
}
