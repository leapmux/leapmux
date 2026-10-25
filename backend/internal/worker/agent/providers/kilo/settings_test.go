package kilo

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

// The fake server follows Kilo's own rules (packages/opencode/src/acp/service.ts of Kilo):
//
//   - A model write always resets the effort to the first variant of the model, even for
//     the model that already runs. Kilo has no keep rule.
//   - The server sends no config_option_update. The reply to the write is the only snapshot.
//   - A write of an effort that the current model lacks fails with -32602.
//   - A model without variants has no effort option.
//
// The worker merges the stored effort into a model switch, so UpdateSettings receives
// {model: B, effort: X}.

const (
	kiloModelA = "leapmux/model-a"
	kiloModelB = "leapmux/model-b"
)

func newKiloSwitchServer(variantsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: agent.OptionIDEffort,
		Models:   []string{kiloModelA, kiloModelB},
		Levels:   map[string][]string{kiloModelA: {"low", "medium", "high"}, kiloModelB: variantsOfB},
	}
	server.Reset = func(model, _ string) string { return server.Levels[model][0] }
	server.Start(kiloModelA, "high")
	return server
}

func newKiloSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, false), requests
}

func TestKiloModelSwitchWritesTheChosenEffortAfterTheNativeReset(t *testing.T) {
	t.Parallel()
	server := newKiloSwitchServer("low", "medium", "high")
	ag, _, requests := newKiloSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: kiloModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=" + kiloModelB, "effort=high"}, acptest.ConfigWrites(requests()))
	assert.Equal(t, "high", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "high", *result.Settlements[agent.OptionIDEffort].Value)
}

func TestKiloModelSwitchToAModelWithoutVariantsWritesNoEffort(t *testing.T) {
	t.Parallel()
	server := newKiloSwitchServer()
	ag, _, requests := newKiloSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: kiloModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses an effort write for a model without variants")
	assert.Equal(t, []string{"model=" + kiloModelB}, acptest.ConfigWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))
}

func TestKiloSwarmLaunchValue(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		swarm, err := kiloSwarmFromOptions(optionmap.Map{OptionSwarm: value})
		require.NoError(t, err, value)
		assert.Equal(t, want, swarm, value)
		env, err := kiloLaunchEnv(agent.Options{Options: map[string]string{OptionSwarm: value}})
		require.NoError(t, err, value)
		assert.Equal(t, []string{kiloSharedAgentBoardEnv + "=" + strconv.FormatBool(want)}, env, "the start pins the value either way, over an inherited one")
	}
	_, err := kiloSwarmFromOptions(optionmap.Map{OptionSwarm: "yes"})
	require.Error(t, err)
	_, err = kiloLaunchEnv(agent.Options{Options: map[string]string{OptionSwarm: "yes"}})
	require.Error(t, err)
	_, err = Start(t.Context(), agent.Options{AgentID: "kilo-invalid-swarm", Options: map[string]string{OptionSwarm: "yes"}}, agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, "kilo swarm")
}

func TestKiloRegistrationServesKiloSwarm(t *testing.T) {
	t.Parallel()
	group := optionids.GroupByID(Registration().OptionGroups, OptionSwarm)
	require.NotNil(t, group)
	assert.Equal(t, "true", group.GetDefaultValue(), "Kilo turns its board on by default")
	assert.True(t, group.GetMutable())
}

// newSwarmAgent builds the agent that Start builds for the launch value swarm.
func newSwarmAgent(t *testing.T, swarm bool) (*Agent, *agenttest.Sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := newAgent(swarm)
	a.AttachPeerForTest(ctx, cancel, &agenttest.Stdin{}, "kilo-swarm", "session-1")
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
	return a, sink
}

func TestKiloSwarmChangeRestartsTheAgent(t *testing.T) {
	t.Parallel()
	a, _ := newSwarmAgent(t, true)
	assert.Equal(t, "true", optionids.GroupByID(a.OptionGroups(), OptionSwarm).GetCurrentValue())

	restart := a.UpdateSettings(optionmap.Map{OptionSwarm: "false"})
	assert.Equal(t, agent.OptionSettlementUnresolved, restart.Settlements[OptionSwarm].State, "Kilo reads the switch only at start")

	same := a.UpdateSettings(optionmap.Map{OptionSwarm: "true"})
	assert.Equal(t, "true", same.ConfirmedOptions()[OptionSwarm], "an unchanged value needs no restart")

	invalid := a.UpdateSettings(optionmap.Map{OptionSwarm: "yes"})
	assert.Equal(t, "true", invalid.ConfirmedOptions()[OptionSwarm], "an invalid value is refused with the running value")
}

// configDaemon serves Kilo's configuration route with body.
func configDaemon(t *testing.T, status int, body string) *opencodetest.FakeDaemon {
	t.Helper()
	daemon := opencodetest.NewFakeDaemon(t, opencode.EventRoute, opencode.QuestionRoot)
	daemon.HandleFunc(kiloConfigRoute, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
	return daemon
}

func TestKiloSwarmFollowsAConfigurationThatTurnsItOff(t *testing.T) {
	t.Parallel()
	a, sink := newSwarmAgent(t, true)
	daemon := configDaemon(t, http.StatusOK, `{"shared_agent_board":false,"model":"leapmux/model-a"}`)
	a.BeginQuestionsForTest(t.Context(), a.Sink(), "kilo-swarm", daemon.Server.URL)
	testutil.RequireEventually(t, func() bool { return sink.StatusActiveCount() == 1 }, "the changed catalog is published")
	group := optionids.GroupByID(a.OptionGroups(), OptionSwarm)
	require.NotNil(t, group)
	assert.Equal(t, "false", group.GetCurrentValue())
	assert.False(t, group.GetMutable())
	assert.Equal(t, kiloSwarmConfigOffReason, group.GetReadOnlyReason())
	require.Len(t, group.GetOptions(), 1)
	assert.Equal(t, "false", group.GetOptions()[0].GetId())

	refused := a.UpdateSettings(optionmap.Map{OptionSwarm: "true"})
	assert.Equal(t, "false", refused.ConfirmedOptions()[OptionSwarm], "Kilo's own configuration wins over On")
	kept := a.UpdateSettings(optionmap.Map{OptionSwarm: "false"})
	assert.Equal(t, "false", kept.ConfirmedOptions()[OptionSwarm], "Off needs no restart while the configuration turns the board off")
}

func TestKiloSwarmKeepsItsValueForAnyOtherConfiguration(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]struct {
		status int
		body   string
	}{
		"a configuration that turns it on": {status: http.StatusOK, body: `{"shared_agent_board":true}`},
		"a configuration without the key":  {status: http.StatusOK, body: `{"model":"leapmux/model-a"}`},
		"an unreadable configuration":      {status: http.StatusOK, body: `not JSON`},
		"a refused read":                   {status: http.StatusServiceUnavailable, body: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newSwarmAgent(t, false)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			a.readSwarmConfig(ctx)
			daemon := configDaemon(t, answer.status, answer.body)
			a.BeginQuestionsForTest(ctx, a.Sink(), "kilo-swarm", daemon.Server.URL)
			// The bridge reads on its own goroutine, and this read reads the same answer. Neither changes the value.
			a.readSwarmConfig(ctx)
			assert.Zero(t, sink.StatusActiveCount())
			group := optionids.GroupByID(a.OptionGroups(), OptionSwarm)
			assert.Equal(t, "false", group.GetCurrentValue())
			assert.True(t, group.GetMutable())
		})
	}
}
