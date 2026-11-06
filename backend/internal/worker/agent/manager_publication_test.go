package agent_test

import (
	"context"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type managerPublicationProcess struct {
	agenttest.IdleAgent
	stopped chan struct{}
	once    sync.Once
}

func (process *managerPublicationProcess) Stop() {
	process.once.Do(func() { close(process.stopped) })
}

func (process *managerPublicationProcess) IsStopped() bool {
	select {
	case <-process.stopped:
		return true
	default:
		return false
	}
}

func (process *managerPublicationProcess) Wait() error {
	<-process.stopped
	return nil
}

type managerPublicationServices struct {
	agent.ProviderServices
	publishGoal    func()
	publishProcess func()
}

func (services managerPublicationServices) PublishGoalCapabilities() {
	if services.publishGoal != nil {
		services.publishGoal()
	}
}

func (services managerPublicationServices) PublishInputProcessState() {
	if services.publishProcess != nil {
		services.publishProcess()
	}
}

func TestManagerStartupPublicationsAllowSynchronousProcessExit(t *testing.T) {
	for _, publication := range []string{"goal capabilities", "input process state"} {
		t.Run(publication, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			manager := agent.NewManager(testRegistry, nil)
			const id = "publication-stop"
			process := &managerPublicationProcess{stopped: make(chan struct{})}
			t.Cleanup(func() {
				process.Stop()
				manager.StopAndWaitAgent(id)
			})
			exited := make(chan struct{})
			completedBeforePublicationReturned := false
			observe := func() {
				go func() {
					manager.StopAndWaitAgent(id)
					close(exited)
				}()
				select {
				case <-exited:
					completedBeforePublicationReturned = true
				case <-ctx.Done():
				}
			}
			services := managerPublicationServices{ProviderServices: agenttest.Nop()}
			if publication == "goal capabilities" {
				services.publishGoal = observe
			} else {
				services.publishProcess = observe
			}
			_, err := manager.StartAgentWith(t.Context(), agent.Options{AgentID: id, WorkingDir: t.TempDir()}, services,
				func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
					return process, nil
				})
			require.NoError(t, err)
			// Start returned, so even the old order can now run exit cleanup.
			// Join it before checking the order and ending the test.
			<-exited
			assert.True(t, completedBeforePublicationReturned, "exit cleanup must run while a startup publication waits for it")
			assert.False(t, manager.HasAgent(id))
		})
	}
}
