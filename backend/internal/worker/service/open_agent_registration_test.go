package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// startupRegistrationFacts is what a reader learns about a new agent at the
// instant that its row becomes visible.
type startupRegistrationFacts struct {
	// rowVisible is false when the row did not exist, which a probe reports as
	// a test defect instead of a product defect.
	rowVisible bool
	// registry is the status that the startup registry reports for the agent.
	registry leapmuxv1.AgentStatus
	// tracked is whether the registry holds an entry at all.
	tracked bool
	// listed is the status that a ListAgents reply gives for the agent.
	listed leapmuxv1.AgentStatus
}

// probeStartupRegistration reads the new agent the way a reader reads it: through
// the startup registry, and through a ListAgents call. The caller runs it at the
// instant that the row becomes visible.
func probeStartupRegistration(t *testing.T, svc *Service, d *channel.Dispatcher, agentID string) startupRegistrationFacts {
	t.Helper()
	var facts startupRegistrationFacts
	_, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	facts.rowVisible = err == nil
	facts.registry, _, _, facts.tracked = svc.AgentStartup.status(agentID)

	w := newTestWriter()
	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	facts.listed = resp.GetAgents()[0].GetStatus()
	return facts
}

// A reader that finds the row of a new agent must find the startup too. The
// startup registry is the only source of STARTING. A row with no entry and no
// process reads as INACTIVE, so a reader that runs between the two writes takes
// an agent that is about to start for an agent that never will.
func TestOpenAgent_RegistersTheStartupBeforeTheRowIsVisible(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New("forced start failure")
	}

	var atCreate startupRegistrationFacts
	svc.createAgentRecordFn = func(ctx context.Context, params db.CreateAgentParams) error {
		if err := svc.Queries.CreateAgent(ctx, params); err != nil {
			return err
		}
		atCreate = probeStartupRegistration(t, svc, d, params.ID)
		return nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	waitForStartupFailure(t, svc, resp.GetAgent().GetId())

	require.True(t, atCreate.rowVisible, "the probe must run when the row exists")
	assert.True(t, atCreate.tracked, "the startup registry must hold the agent when its row becomes visible")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, atCreate.registry,
		"the startup registry must report STARTING when the row becomes visible")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, atCreate.listed,
		"ListAgents must report STARTING, never INACTIVE, when the row becomes visible")
}

// A row that was never created owns no startup. The registry holds the startup
// from before the write, so a failed write must give it back.
func TestOpenAgent_ReleasesTheStartupWhenTheRowCannotBeCreated(t *testing.T) {
	t.Parallel()

	// No drainAllInFlight: no startup goroutine runs, and a leaked startup must
	// fail the test at requireStartupsReleased. A drain would wait for it for ever.
	svc, d, w := setupTestService(t)
	var attempted string
	svc.createAgentRecordFn = func(_ context.Context, params db.CreateAgentParams) error {
		attempted = params.ID
		return errors.New("forced create failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, "failed to create agent", w.errors[0].message)
	require.NotEmpty(t, attempted)
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
	_, _, _, tracked := svc.AgentStartup.status(attempted)
	assert.False(t, tracked)
	requireNoCleanupClaim(t, &svc.agentCleanups, attempted)
	_, err := svc.Queries.GetAgentByID(context.Background(), attempted)
	assert.ErrorIs(t, err, sql.ErrNoRows, "a failed create leaves no row")
}

// The resume path creates the row in a transaction, and its unique index refuses
// a second open of one native session. That refusal is the other failure of the
// write, and it must give the startup back as well.
func TestOpenAgent_ReleasesTheStartupWhenTheNativeSessionIsAlreadyOpen(t *testing.T) {
	t.Parallel()

	// No drain at cleanup, for the reason that the first test states.
	svc, d, _ := setupTestService(t)
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	workingDir := t.TempDir()
	seedTranscriptSource(t, svc, "open-agent", workingDir, "session-a1", provider, false)

	w := newTestWriter()
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir: workingDir, AgentProvider: provider, AgentSessionId: "session-a1",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "already open")
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
	assert.Equal(t, 1, countAgentRows(t, svc), "a refused handle creates no second agent")
}

// The row exists once the handler reads it back, so a failed read leaves a row
// and no startup: nothing will ever run for the row, and the claim of its
// cleanup must go too. The startup registry must not report STARTING for it.
func TestOpenAgent_ReleasesTheStartupWhenTheCreatedRowCannotBeRead(t *testing.T) {
	t.Parallel()

	// No drain, for the reason that the first test states.
	svc, d, w := setupTestService(t)
	var created string
	svc.createAgentRecordFn = func(ctx context.Context, params db.CreateAgentParams) error {
		if err := svc.Queries.CreateAgent(ctx, params); err != nil {
			return err
		}
		created = params.ID
		return nil
	}
	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		if agentID == created {
			return db.Agent{}, errors.New("forced read failure")
		}
		return fetch(ctx, agentID)
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, "failed to fetch created agent", w.errors[0].message)
	require.NotEmpty(t, created)
	requireStartupsReleased(t, &svc.AgentStartup.startupCore)
	_, _, _, tracked := svc.AgentStartup.status(created)
	assert.False(t, tracked)
	requireNoCleanupClaim(t, &svc.agentCleanups, created)
}
