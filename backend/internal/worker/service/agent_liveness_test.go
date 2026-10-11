package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// liveness samples the startup registry for agentID, then reports running as the
// process state. It takes the shape that sampleAgentLiveness takes from the
// Manager, so a test states the process without a real process.
func liveness(svc *Service, agentID string, running bool) agentLiveness {
	return svc.sampleAgentLiveness(agentID, func() bool { return running })
}

// sampleAgentLiveness reads the startup registry first and the process second.
// A startup ends with the process registered and then the entry dropped, so this
// order finds the entry or finds the process. The probe stands for the process
// read. It ends the startup, and then it states what the Manager holds.
func TestSampleAgentLiveness_ReadsTheRegistryBeforeTheProcess(t *testing.T) {
	t.Parallel()

	const id = "agent-1"
	for _, tc := range []struct {
		name string
		// startup says whether a startup is in flight when the sample begins.
		startup bool
		// endsInProbe says whether that startup ends while the probe runs.
		endsInProbe bool
		// processAtProbe is the answer of the Manager after the probe's events.
		processAtProbe bool
		wantStatus     leapmuxv1.AgentStatus
	}{
		{"a startup in flight with no process yet", true, false, false, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING},
		{"a startup that ends with its process while the sample runs", true, true, true, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a startup that ends and loses its process while the sample runs", true, true, false, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING},
		{"no startup and no process", false, false, false, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE},
		{"no startup and a running process", false, false, true, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, _, _ := setupTestService(t)
			var handle *startupEntry
			if tc.startup {
				handle = svc.AgentStartup.begin(id, func() {})
				require.NotNil(t, handle)
			}
			probes := 0
			live := svc.sampleAgentLiveness(id, func() bool {
				probes++
				if tc.endsInProbe {
					svc.AgentStartup.succeed(id, handle)
				}
				return tc.processAtProbe
			})
			if handle != nil {
				svc.AgentStartup.finishEntry(handle)
			}

			assert.Equal(t, 1, probes, "the sample must ask for the process exactly once")
			status, _, _ := deriveAgentStatus(&db.Agent{ID: id}, live)
			assert.Equal(t, tc.wantStatus, status)
		})
	}
}

// A process replacement claims its hold BEFORE it stops the old process. A reader
// that read the registry before the claim and reads the process after the stop
// finds no entry and no process, and it reads INACTIVE for an agent that is
// restarting. The sample therefore reads the registry again when both reads found
// nothing. The probe stands for the read of the process: it runs the events that
// happen between the two reads, and then it states what the Manager holds.
func TestSampleAgentLiveness_ReadsStartingForAReplacementThatBeginsDuringTheSample(t *testing.T) {
	t.Parallel()

	const id = "agent-1"
	for _, tc := range []struct {
		name string
		// duringProbe runs the replacement between the two reads. It returns
		// the answer of the Manager at the read of the process.
		duringProbe func(t *testing.T, svc *Service) bool
		wantStatus  leapmuxv1.AgentStatus
	}{
		{"a replacement that claims its hold and stops the old process", func(t *testing.T, svc *Service) bool {
			hold := svc.holdRelaunch(id, holdTestProvider)
			require.NotNil(t, hold.handle)
			t.Cleanup(hold.release)
			return false
		}, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING},
		{"a replacement that ends with its new process", func(t *testing.T, svc *Service) bool {
			hold := svc.holdRelaunch(id, holdTestProvider)
			require.NotNil(t, hold.handle)
			hold.release()
			return true
		}, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a replacement that gives up and leaves no process", func(t *testing.T, svc *Service) bool {
			hold := svc.holdRelaunch(id, holdTestProvider)
			require.NotNil(t, hold.handle)
			hold.release()
			return false
		}, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, _, _ := setupTestService(t)
			probes := 0
			live := svc.sampleAgentLiveness(id, func() bool {
				probes++
				return tc.duringProbe(t, svc)
			})

			assert.Equal(t, 1, probes, "the sample must ask for the process exactly once")
			status, _, _ := deriveAgentStatus(&db.Agent{ID: id}, live)
			assert.Equal(t, tc.wantStatus, status)
		})
	}
}

// A failed startup keeps its error and its message in the sample, so a reader
// that builds the reply from the sample reports the same STARTUP_FAILED that the
// registry reports.
func TestSampleAgentLiveness_CarriesTheFailureOfAStartup(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	handle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, handle)
	svc.AgentStartup.fail(handle, "claude: command not found")
	svc.AgentStartup.finishEntry(handle)

	status, startupError, startupMessage := deriveAgentStatus(&db.Agent{ID: "agent-1"}, liveness(svc, "agent-1", false))

	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, "claude: command not found", startupError)
	assert.Empty(t, startupMessage)
}

// The message of a startup in flight reaches the reply, so a client that opens a
// tab in the middle of a startup shows the phase that the startup is in.
func TestSampleAgentLiveness_CarriesTheMessageOfAStartupInFlight(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	handle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, handle)
	svc.AgentStartup.setMessage("agent-1", "Starting Claude Code…")

	status, startupError, startupMessage := deriveAgentStatus(&db.Agent{ID: "agent-1"}, liveness(svc, "agent-1", false))

	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, status)
	assert.Empty(t, startupError)
	assert.Equal(t, "Starting Claude Code…", startupMessage)
	svc.AgentStartup.abandon(handle)
}
