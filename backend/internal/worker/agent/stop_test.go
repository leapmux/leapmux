package agent_test

import (
	"context"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stopTargetProvider struct {
	agenttest.IdleAgent
	inputs *[]string
	values []string
	groups []*leapmuxv1.AvailableOptionGroup
	read   func()
}

func (p stopTargetProvider) Interrupt(stop agent.StopContext) error {
	*p.inputs = append(*p.inputs, "interrupt")
	stop.ReportIgnored()
	return nil
}

func (p stopTargetProvider) InterruptChild(childKey string, stop agent.StopContext) error {
	*p.inputs = append(*p.inputs, "child:"+childKey)
	stop.ReportIgnored()
	return nil
}

func (p stopTargetProvider) SendRawInput(data []byte, stop agent.StopContext) error {
	*p.inputs = append(*p.inputs, string(data))
	stop.ReportIgnored()
	return nil
}

func (p stopTargetProvider) InterruptEscalationReady() bool { return true }

func (p stopTargetProvider) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	if p.read != nil {
		p.read()
	}
	return p.groups
}

func TestStopContextReportsOnlyItsOwnCallback(t *testing.T) {
	t.Parallel()
	var zero agent.StopContext
	assert.NotPanics(t, zero.ReportIgnored)
	var first, second int
	a := agent.NewStopContext(func() { first++ })
	b := agent.NewStopContext(func() { second++ })
	a.ReportIgnored()
	assert.Equal(t, 1, first)
	assert.Zero(t, second)
	b.ReportIgnored()
	assert.Equal(t, 1, first)
	assert.Equal(t, 1, second)
}

func TestStopTargetKeepsANonComparableRegistration(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	var oldInputs, newInputs []string
	old := stopTargetProvider{inputs: &oldInputs, values: []string{"non-comparable"}}
	m.PutAgentForTest("value", old)
	target, err := m.CaptureStopTarget("value")
	require.NoError(t, err)
	require.True(t, target.IsCurrent())
	require.True(t, target.EscalationReady())
	var ignored int
	stop := agent.NewStopContext(func() { ignored++ })
	require.NoError(t, target.Interrupt(stop))
	require.NoError(t, target.InterruptChild("child", stop))
	require.NoError(t, target.SendRawInput([]byte(" unchanged bytes \n"), stop))
	assert.Equal(t, 3, ignored)
	m.PutAgentForTest("value", stopTargetProvider{inputs: &newInputs, values: []string{"replacement"}})
	assert.False(t, target.IsCurrent())
	require.NoError(t, target.Interrupt(stop))
	assert.Equal(t, []string{"interrupt", "child:child", " unchanged bytes \n", "interrupt"}, oldInputs)
	assert.Empty(t, newInputs, "a captured target never resolves the replacement")
}

func TestStopTargetRejectsAnAbsentRegistration(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	_, err := m.CaptureStopTarget("missing")
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
	var target agent.StopTarget
	assert.False(t, target.IsCurrent())
	assert.False(t, target.EscalationReady())
	assert.ErrorIs(t, target.Interrupt(agent.StopContext{}), agent.ErrAgentNotFound)
	assert.ErrorIs(t, target.InterruptChild("child", agent.StopContext{}), agent.ErrAgentNotFound)
	assert.ErrorIs(t, target.SendRawInput(nil, agent.StopContext{}), agent.ErrAgentNotFound)
	m.PutAgentForTest("no-child", agenttest.IdleAgent{})
	target, err = m.CaptureStopTarget("no-child")
	require.NoError(t, err)
	assert.True(t, errors.Is(target.InterruptChild("child", agent.StopContext{}), agent.ErrChildOperationUnsupported))
}

func TestLiveCatalogRejectsAReplacedNonComparableRegistration(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	oldGroups := []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, CurrentValue: "old"}}
	newGroups := []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, CurrentValue: "new"}}
	m.PutAgentForTest("value", stopTargetProvider{values: []string{"old"}, groups: oldGroups, read: func() { close(entered); <-release }})
	done := make(chan []*leapmuxv1.AvailableOptionGroup, 1)
	go func() { done <- m.LiveOptionGroups("value", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE) }()
	<-entered
	m.PutAgentForTest("value", stopTargetProvider{values: []string{"new"}, groups: newGroups})
	close(release)
	assert.Empty(t, <-done)
	assert.Equal(t, "new", m.LiveOptionGroups("value", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0].CurrentValue)
}

func TestStopTargetRefusesAnExitingProvider(t *testing.T) {
	t.Parallel()
	exited, release := make(chan struct{}), make(chan struct{})
	m := agent.NewManager(testRegistry, func(string, int, error, bool) { close(exited); <-release })
	_, err := m.StartAgentWith(t.Context(), agent.Options{AgentID: "exiting", WorkingDir: t.TempDir()}, agenttest.Nop(),
		func(_ context.Context, _ agent.Options, _ agent.ProviderServices) (agent.Agent, error) {
			return stopTargetProvider{values: []string{"non-comparable"}}, nil
		})
	require.NoError(t, err)
	<-exited
	_, err = m.CaptureStopTarget("exiting")
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
	assert.False(t, m.AgentAlive("exiting"))
	close(release)
	m.StopAndWaitAgent("exiting")
}
