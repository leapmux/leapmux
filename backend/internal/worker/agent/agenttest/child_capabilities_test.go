package agenttest

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
)

// statingPlugin states the child capabilities that a test chooses.
type statingPlugin struct {
	agentapi.ProviderDefaults
	steers, interrupts bool
}

func (p statingPlugin) ChildCapabilities(optionmap.Map) agentapi.ChildCapabilities {
	return agentapi.ChildCapabilities{AcceptsMessages: p.steers, AcceptsInterrupt: p.interrupts}
}

// plainAgent has no child capability.
type plainAgent struct{}

// interruptingAgent can interrupt a child turn, and nothing more.
type interruptingAgent struct{}

func (*interruptingAgent) InterruptChild(childKey string, stop agentapi.StopContext) error {
	return nil
}

// steeringAgent can do both.
type steeringAgent struct{ interruptingAgent }

func (*steeringAgent) SendChildInput(string, agentapi.ChildInput) (agentapi.ChildInputResult, error) {
	return agentapi.ChildInputResult{StartsTurn: true}, nil
}
func (*steeringAgent) SteerChildInput(string, agentapi.SteerRequest) (agentapi.NativeInputReceipt, error) {
	return agentapi.NativeInputReceipt{}, nil
}
func (*steeringAgent) ChildSteerPolicy() agentapi.SteerPolicy         { return agentapi.SteerPolicy{} }
func (*steeringAgent) ActiveChildTurnState(string) agentapi.TurnState { return agentapi.TurnState{} }

func TestAssertChildCapabilitiesPassesWhenThePluginStatesTheMethodSet(t *testing.T) {
	t.Parallel()
	AssertChildCapabilities(t, statingPlugin{}, (*plainAgent)(nil))
	AssertChildCapabilities(t, statingPlugin{interrupts: true}, (*interruptingAgent)(nil))
	AssertChildCapabilities(t, statingPlugin{steers: true, interrupts: true}, (*steeringAgent)(nil))
}

// The drift that the suite exists for, in both directions and for both
// capabilities.
func TestAssertChildCapabilitiesFailsWhenThePluginAndTheAgentDisagree(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		plugin statingPlugin
		agent  any
	}{
		"an interrupter that states nothing": {statingPlugin{}, (*interruptingAgent)(nil)},
		"a claimed interrupt with no method": {statingPlugin{interrupts: true}, (*plainAgent)(nil)},
		"a steerer that states no steering":  {statingPlugin{interrupts: true}, (*steeringAgent)(nil)},
		"a claimed steering with no method":  {statingPlugin{steers: true, interrupts: true}, (*interruptingAgent)(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			probe := &recordingTB{}
			AssertChildCapabilities(probe, tc.plugin, tc.agent)
			assert.True(t, probe.failed, "the suite must report the disagreement")
		})
	}
}

// recordingTB records a failure instead of failing the test that runs it. The
// suites and testify call only Helper, Name, Errorf and FailNow, so the
// embedded TB stays nil. FailNow ends the calling goroutine, as testing's own
// does, so a suite that can call it runs in its own goroutine (failsUnder).
type recordingTB struct {
	testing.TB
	failed bool
}

func (*recordingTB) Helper() {}

func (*recordingTB) Name() string { return "probe" }

func (r *recordingTB) Errorf(string, ...any) { r.failed = true }

func (r *recordingTB) FailNow() {
	r.failed = true
	runtime.Goexit()
}

// failsUnder reports whether check fails the TB that it receives. It runs
// check in its own goroutine, because a FailNow ends that goroutine.
func failsUnder(check func(testing.TB)) bool {
	probe := &recordingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		check(probe)
	}()
	<-done
	return probe.failed
}
