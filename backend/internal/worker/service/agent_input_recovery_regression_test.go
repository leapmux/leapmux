package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputRecoveryDiscoveryOnlyReturnsAffectedOpenProcesses(t *testing.T) {
	t.Parallel()
	svc, _ := newInputRecoveryService(t, func(int) func(int) error { return func(int) error { return fmtRestartRequired() } })
	for _, id := range []string{"a-stale", "b-stale", "c-healthy", "d-closed", "e-archived", "f-other-provider"} {
		seedRecoveryAgent(t, svc, id, id+"-thread")
		if id == "f-other-provider" {
			_, err := svc.DB.ExecContext(t.Context(), "UPDATE agents SET agent_provider = ? WHERE id = ?", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, id)
			require.NoError(t, err)
		}
		require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
		if id != "c-healthy" {
			require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
		}
		if id == "d-closed" {
			_, err := svc.Queries.CloseAgent(t.Context(), id)
			require.NoError(t, err)
		}
		if id == "e-archived" {
			_, err := svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{ID: id, WorkspaceArchived: true})
			require.NoError(t, err)
		}
	}
	targets, err := svc.listAgentInputRecoveries(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	assert.Equal(t, "a-stale", targets[0].AgentId)
	assert.Equal(t, "b-stale", targets[1].AgentId)
	for _, target := range targets {
		require.NotNil(t, target.Recovery)
		assert.Positive(t, target.Recovery.OutputGeneration)
		assert.NotEmpty(t, target.Recovery.ProcessInstanceId)
		assert.Equal(t, target.AgentId+"-thread", target.Recovery.AgentSessionId)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, target.Recovery.AgentProvider)
	}
	for _, invalid := range []leapmuxv1.AgentProvider{0, -1, 999999} {
		_, err := svc.listAgentInputRecoveries(invalid)
		require.ErrorIs(t, err, inputqueue.ErrInvalidInput)
	}
}

func TestInputRecoveryPreservesConversationQuestionsAndRetiresBlockingControls(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const root = "recovery-control-root"
	seedRecoveryAgent(t, svc, root, "saved-thread")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(root), agent.ErrInputProcessRestartRequired)
	parent := root
	for _, child := range []string{"control-child", "control-grandchild"} {
		require.NoError(t, svc.Queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{ID: child, ParentAgentID: sql.NullString{String: parent, Valid: true}, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}))
		parent = child
	}
	_, err := svc.Queries.CloseAgent(t.Context(), parent)
	require.NoError(t, err)
	for _, owner := range []string{root, parent} {
		for _, nonblocking := range []bool{false, true} {
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{AgentID: owner, RequestID: fmt.Sprintf("control-%t", nonblocking), Payload: []byte(`{}`), NonBlocking: nonblocking})
		}
	}
	for _, owner := range []string{root, parent} {
		controls, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), owner)
		require.NoError(t, err)
		require.Len(t, controls, 2, "approval has not stopped their process")
	}
	assert.False(t, starts.fake(0).wasStopped())
	approveRecoveryInput(t, svc, root)
	for _, owner := range []string{root, parent} {
		controls, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), owner)
		require.NoError(t, err)
		require.Len(t, controls, 1)
		assert.True(t, controls[0].NonBlocking, "the same conversation retains its question")
	}
}

func TestInputRecoveryPreservesQueuedAttachments(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "attachment-recovery"
	seedRecoveryAgent(t, svc, id, "attachment-thread")
	_, err := svc.InputQueue.Enqueue(t.Context(), inputqueue.NewItem{
		ID: "attachment-input", AgentID: id, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "read the attachment", Attachments: []inputqueue.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("retained attachment")}},
	})
	require.NoError(t, err)
	recoveryQueuedItem(t, svc, id, "attachment-input")
	approveRecoveryInput(t, svc, id)
	waitForMessageCount(t, svc, id, 1)
	require.Equal(t, 2, starts.count())
	stale := starts.fake(0)
	texts, _ := stale.sent()
	assert.Empty(t, texts)
	replacement := starts.fake(1)
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	require.Len(t, replacement.sentAttachments, 1)
	require.Len(t, replacement.sentAttachments[0], 1)
	attachment := replacement.sentAttachments[0][0]
	assert.Equal(t, "notes.txt", attachment.Filename)
	assert.Equal(t, "text/plain", attachment.MimeType)
	assert.Equal(t, []byte("retained attachment"), attachment.Data)
}

type inputRecoveryDeliveryAgent struct {
	*recoveryAgent
}

func (a *inputRecoveryDeliveryAgent) SupportsSteering() bool { return true }

func (a *inputRecoveryDeliveryAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.SendInput(content, attachments)
}

func (a *inputRecoveryDeliveryAgent) SteerInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.SendInputForSession(sessionID, content, attachments)
}

func (a *inputRecoveryDeliveryAgent) CompactContext() error {
	return a.SendInput("compact", nil)
}

func TestInputRecoveryPreparesTheProviderSelectedForDelivery(t *testing.T) {
	t.Parallel()
	for _, operation := range []struct {
		name      string
		validated bool
		deliver   func(*Service, string) error
	}{
		{name: "send", deliver: func(svc *Service, id string) error {
			return svc.Agents.SendInput(id, "input", nil)
		}},
		{name: "session send", deliver: func(svc *Service, id string) error {
			p, release := svc.Agents.LockProvider(id)
			release()
			return agent.SendInputToSession(p, "delivery-thread", "input", nil)
		}},
		{name: "steer", deliver: func(svc *Service, id string) error {
			return svc.Agents.SteerInput(id, "input", nil)
		}},
		{name: "compact", deliver: func(svc *Service, id string) error {
			return svc.Agents.CompactContext(id)
		}},
		{name: "validated active answer", validated: true, deliver: func(svc *Service, id string) error {
			p, release := svc.Agents.LockProvider(id)
			release()
			return agent.SteerInputToSession(p, "delivery-thread", "answer", nil)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			signIn := errors.New("the replacement requires sign-in")
			svc, starts := newInputRecoveryService(t, func(launch int) func(int) error {
				if launch == 2 {
					return func(int) error { return signIn }
				}
				return nil
			})
			svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
				return svc.Agents.StartAgentWith(ctx, opts, sink, func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
					running, err := starts.start(ctx, opts, sink)
					if err != nil {
						return nil, err
					}
					return &inputRecoveryDeliveryAgent{recoveryAgent: running.(*recoveryAgent)}, nil
				})
			}
			const agentID = "delivery-agent"
			seedRecoveryAgent(t, svc, agentID, "delivery-thread")
			require.NoError(t, svc.ensureAgentReadyForInput(agentID))
			unlock := svc.Agents.LockAgent(agentID)
			svc.Agents.StopAndWaitAgent(agentID)
			unlock()
			require.NoError(t, svc.ensureAgentRunning(agentID, resumeStoredSession, interactiveStart))
			err := operation.deliver(svc, agentID)
			replacement := starts.fake(1)
			texts, _ := replacement.sent()
			if operation.validated {
				require.NoError(t, err)
				assert.Zero(t, replacement.preparations())
				assert.Equal(t, []string{"answer"}, texts)
			} else {
				require.ErrorIs(t, err, signIn)
				assert.Equal(t, 1, replacement.preparations())
				assert.Empty(t, texts, "the actual delivery target must prepare before accepting input")
			}
		})
	}
}

func TestInputRecoveryPreparedRegistrationMustRemainCurrent(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name        string
		result      error
		replacement bool
	}{
		{name: "success", replacement: true},
		{name: "ordinary failure", result: assert.AnError, replacement: true},
		{name: "restart request", result: fmtRestartRequired(), replacement: true},
		{name: "process disappeared"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			defer finish()
			svc, starts := newInputRecoveryService(t, func(launch int) func(int) error {
				if launch == 1 {
					return func(int) error {
						close(entered)
						<-release
						return scenario.result
					}
				}
				return nil
			})
			const agentID = "prepared-agent"
			seedRecoveryAgent(t, svc, agentID, "prepared-thread")
			require.NoError(t, svc.ensureAgentRunning(agentID, resumeStoredSession, interactiveStart))
			done := make(chan error, 1)
			go func() {
				err := svc.ensureAgentReadyForInput(agentID)
				if err == nil {
					err = svc.Agents.SendInput(agentID, "unchecked input", nil)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the original provider did not enter preparation")
			}
			unlock := svc.Agents.LockAgent(agentID)
			svc.Agents.StopAndWaitAgent(agentID)
			unlock()
			if scenario.replacement {
				require.NoError(t, svc.ensureAgentRunning(agentID, resumeStoredSession, interactiveStart))
			}
			finish()
			var err error
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("the original preparation did not settle")
			}
			var delivery *inputqueue.DeliveryError
			require.ErrorAs(t, err, &delivery, "a retired preparation cannot authorize its successor")
			assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
			if scenario.replacement {
				replacement := starts.fake(1)
				require.NotNil(t, replacement)
				assert.False(t, replacement.wasStopped())
				assert.Zero(t, replacement.preparations())
				texts, _ := replacement.sent()
				assert.Empty(t, texts, "the replacement received no input based on the old provider's result")
				require.NoError(t, svc.ensureAgentReadyForInput(agentID))
				assert.Equal(t, 1, replacement.preparations())
			}
		})
	}
}

type inputRecoveryValueAgent struct {
	*recoveryAgent
	labels []string
}

func TestInputRecoveryAcceptsNonComparableValueProvider(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, opts, sink, func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
			running, err := starts.start(ctx, opts, sink)
			if err != nil {
				return nil, err
			}
			if starts.count() == 1 {
				return inputRecoveryValueAgent{recoveryAgent: starts.fake(0), labels: []string{"non-comparable"}}, nil
			}
			return running, nil
		})
	}
	const agentID = "value-agent"
	seedRecoveryAgent(t, svc, agentID, "value-thread")
	var err error
	require.NotPanics(t, func() { err = svc.ensureAgentReadyForInput(agentID) })
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	approveRecoveryInput(t, svc, agentID)
	require.Equal(t, []string{"", "value-thread"}, starts.launches())
	assert.True(t, starts.fake(0).wasStopped())
	assert.False(t, starts.fake(1).wasStopped())
}
