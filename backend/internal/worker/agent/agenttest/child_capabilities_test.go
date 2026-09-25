package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// statingPlugin states the child capabilities that a test chooses.
type statingPlugin struct {
	agent.ProviderDefaults
	steers, interrupts bool
}

func (p statingPlugin) SupportsChildSteering() bool  { return p.steers }
func (p statingPlugin) SupportsChildInterrupt() bool { return p.interrupts }

// plainAgent has no child capability.
type plainAgent struct{}

// interruptingAgent can interrupt a child turn, and nothing more.
type interruptingAgent struct{}

func (*interruptingAgent) InterruptChild(string) error { return nil }

// steeringAgent can do both.
type steeringAgent struct{ interruptingAgent }

func (*steeringAgent) SendChildInput(string, string, []*leapmuxv1.Attachment) error  { return nil }
func (*steeringAgent) SteerChildInput(string, string, []*leapmuxv1.Attachment) error { return nil }
func (*steeringAgent) ActiveChildTurnState(string) agent.TurnState                   { return agent.TurnState{} }

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
// suite and testify call only Helper, Name and Errorf, so the embedded TB stays
// nil.
type recordingTB struct {
	testing.TB
	failed bool
}

func (*recordingTB) Helper() {}

func (*recordingTB) Name() string { return "probe" }

func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
