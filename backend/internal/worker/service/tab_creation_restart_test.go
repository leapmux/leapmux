package service

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	hubservice "github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type recoveryPublicationHub struct {
	store       store.Store
	journal     crdt.Journal
	manager     *crdt.Manager
	owner       userid.UserID
	workerID    string
	workspaceID string
}

func newRecoveryPublicationHub(t *testing.T) *recoveryPublicationHub {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "recovery-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	workspace := storetest.SeedWorkspace(t, st, owner.ID, "Recovery")
	journal := hubservice.NewCRDTJournal(st)
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		manager := crdt.NewManager(owner, journal, hubservice.NewCRDTAuthChecker(st), nil, time.Now)
		if err := manager.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return manager, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(30 * time.Second) })
	manager, err := registry.Get(t.Context(), owner.ID)
	require.NoError(t, err)
	results, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{BatchId: "seed-recovery", Ops: []*leapmuxv1.CrdtOp{
		{OpId: "recovery-root", Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{NodeId: "root", Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}}}},
		{OpId: "recovery-workspace", Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{WorkspaceId: workspace, RootNodeId: "root"}}},
	}}}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].GetCommitted())
	return &recoveryPublicationHub{store: st, journal: journal, manager: manager, owner: userid.MustNew(owner.ID), workerID: worker.ID, workspaceID: workspace}
}

func (h *recoveryPublicationHub) intent(id string) *leapmuxv1.TabCreation {
	return &leapmuxv1.TabCreation{TabId: id, Placement: &leapmuxv1.TabPlacement{WorkspaceId: h.workspaceID, Destination: &leapmuxv1.TabPlacement_TileId{TileId: "root"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}
}

func (h *recoveryPublicationHub) config(database *sql.DB, directory string) Config {
	return Config{DB: database, WorkerID: h.workerID, SeedRegisteredBy: h.owner.String(), HomeDir: directory, DataDir: directory,
		APITimeout: 30 * time.Second, AgentStartupTimeout: 30 * time.Second,
		PublishTab: func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
			return h.manager.PublishTab(ctx, h.workerID, request)
		},
		ResolveTabPublication: func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
			return h.manager.ResolveTabPublication(ctx, h.workerID, request)
		},
		CancelTabPublication: func(ctx context.Context, request *leapmuxv1.TabPublication) error {
			return h.manager.CancelTabPublication(ctx, h.workerID, request)
		},
	}
}

// newRecoveryService builds fresh managers and registries over a supplied database.
// No runtime state from an earlier Service can satisfy its recovery checks.
func newRecoveryService(t *testing.T, cfg Config) (*Service, *channel.Dispatcher) {
	t.Helper()
	cfg.Send = func(*leapmuxv1.ConnectRequest) error { return nil }
	cfg.Channels = channel.NewManager(nil, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, channel.SendFunc(cfg.Send), nil, 0, 0)
	cfg.Agents = agent.NewManager(testRegistry, nil)
	cfg.Terminals = terminal.NewManager()
	svc := New(cfg)
	svc.ControlIPC = &fakeRemoteIPC{}
	dispatcher := channel.NewDispatcher()
	RegisterAll(dispatcher, svc)
	t.Cleanup(func() {
		dispatcher.CloseAdmission()
		svc.PrivateEvents.Stop()
		svc.Shutdown()
		cfg.Channels.CloseAll()
		require.True(t, dispatcher.WaitHandlers(30*time.Second), "recovery fixture handlers did not join")
	})
	return svc, dispatcher
}

func openRecoveryDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	return database
}

func recoveryCreationRequest(t *testing.T, kind leapmuxv1.TabType, intent *leapmuxv1.TabCreation, directory string) (string, proto.Message) {
	t.Helper()
	method, request := creationRequestForTest(t, kind, intent.GetTabId(), directory)
	switch request := request.(type) {
	case *leapmuxv1.OpenAgentRequest:
		request.Creation = intent
	case *leapmuxv1.OpenTerminalRequest:
		request.Target = &leapmuxv1.OpenTerminalRequest_Creation{Creation: intent}
	case *leapmuxv1.RegisterTabPayloadRequest:
		request.Creation = intent
		request.Payload.WorkingDir = directory
	default:
		t.Fatal("unsupported request in recovery fixture")
	}
	return method, request
}

func dispatchRecoveryRequest(ctx context.Context, svc *Service, dispatcher *channel.Dispatcher, method string, request proto.Message, writer channel.ResponseWriter) {
	payload, err := proto.Marshal(request)
	if err != nil {
		panic(err)
	}
	dispatcher.DispatchWith(ctx, channel.NewCaller(svc.RegisteredBy(), authscope.NonAdminGrant()), &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, writer)
}

func TestCreationRestart_RecoversALostHubAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			hub := newRecoveryPublicationHub(t)
			directory := t.TempDir()
			path := filepath.Join(directory, "worker.db")
			oldDatabase := openRecoveryDatabase(t, path)
			cfg := hub.config(oldDatabase, directory)
			committed := make(chan struct{})
			var once sync.Once
			cfg.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
				response, err := hub.manager.PublishTab(ctx, hub.workerID, request)
				if err != nil || response.GetPublished() == nil {
					return response, err
				}
				once.Do(func() { close(committed) })
				return nil, errors.New("the committed acknowledgement was lost")
			}
			oldService, oldDispatcher := newRecoveryService(t, cfg)
			defer oldService.Shutdown()
			oldStarts := failCreationProcessStarts(oldService)
			id := "lost-ack-" + kind.String()
			method, request := recoveryCreationRequest(t, kind, hub.intent(id), directory)
			writer, done := newTestWriter(), make(chan struct{})
			go func() {
				defer close(done)
				dispatchRecoveryRequest(t.Context(), oldService, oldDispatcher, method, request, writer)
			}()
			ctx := creationOutcomeContext(t)
			select {
			case <-committed:
			case <-done:
				t.Fatalf("creation ended before a Hub commit: %v", writer.rejections())
			case <-ctx.Done():
				t.Fatal("Hub publication did not commit")
			}
			_, before, err := hub.journal.LoadState(ctx, hub.owner.String())
			require.NoError(t, err)
			oldService.Shutdown()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("the old creator did not stop")
			}
			require.Len(t, writer.rejections(), 1)
			assert.NotEqual(t, int32(codes.Aborted), writer.rejections()[0].code)
			assert.Zero(t, oldStarts.Load())
			require.NoError(t, oldDatabase.Close())
			freshDatabase := openRecoveryDatabase(t, path)
			fresh, _ := newRecoveryService(t, hub.config(freshDatabase, directory))
			defer fresh.Shutdown()
			starts := failCreationProcessStarts(fresh)
			require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
			fresh.AgentStartup.WaitForInFlight()
			fresh.TerminalStartup.WaitForInFlight()
			row, err := fresh.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: id})
			require.NoError(t, err)
			assert.Equal(t, creationCommitted, row.State)
			assert.Zero(t, len(row.RequestPayload))
			assert.Zero(t, len(row.RuntimePayload))
			assert.Len(t, hub.manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), 1)
			receipt, err := hub.store.WorkerTabPublications().Get(ctx, store.WorkerTabPublicationKey{UserID: hub.owner, WorkerID: hub.workerID, TabID: id})
			require.NoError(t, err)
			assert.NotEmpty(t, receipt.ResponsePayload)
			_, after, err := hub.journal.LoadState(ctx, hub.owner.String())
			require.NoError(t, err)
			assert.Len(t, after, len(before), "recovery reuses the receipt without a second canonical creation")
			if kind == leapmuxv1.TabType_TAB_TYPE_AGENT || kind == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
				assert.Equal(t, int32(1), starts.Load())
			} else {
				assert.Zero(t, starts.Load())
			}
			switch kind {
			case leapmuxv1.TabType_TAB_TYPE_AGENT:
				resource, err := fresh.Queries.GetAgentByID(ctx, id)
				require.NoError(t, err)
				assert.False(t, resource.ClosedAt.Valid)
			case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
				resource, err := fresh.Queries.GetTerminal(ctx, id)
				require.NoError(t, err)
				assert.False(t, resource.ClosedAt.Valid)
			default:
				_, err := fresh.TabPayloads.Get(ctx, hub.owner.String(), id)
				assert.NoError(t, err)
			}
		})
	}
}

type recoveryCreationSeed struct {
	request     proto.Message
	publication *leapmuxv1.TabPublication
	runtime     creationRuntime
	write       func(context.Context, *db.Queries) error
	state       int64
}

func seedRecoveryCreation(t *testing.T, svc *Service, seed recoveryCreationSeed) *creationOperation {
	t.Helper()
	hash, err := tabcreation.Fingerprint(seed.request)
	require.NoError(t, err)
	op := &creationOperation{coordinator: svc.creations, caller: channel.NewCaller(svc.RegisteredBy(), authscope.NonAdminGrant()), request: seed.request, publication: seed.publication, hash: hash}
	require.NoError(t, op.persist(seed.runtime, seed.write))
	switch seed.state {
	case creationPending:
	case creationPublished:
		response, err := svc.PublishTab(t.Context(), seed.publication)
		require.NoError(t, err)
		require.NotNil(t, response.GetPublished())
		payload, err := proto.Marshal(response)
		require.NoError(t, err)
		changed, err := svc.Queries.PublishTabCreation(t.Context(), db.PublishTabCreationParams{State: creationPublished, PublicationResult: payload, UserID: op.key().owner, WorkerID: svc.WorkerID, TabID: op.key().tabID, State_2: creationPending})
		require.NoError(t, err)
		require.Equal(t, int64(1), changed)
	default:
		t.Fatal("a recovery fixture requires PENDING or PUBLISHED")
	}
	return op
}

func TestCreationRestart_RestoresTheCurrentAgentSessionAndSettings(t *testing.T) {
	t.Parallel()
	for _, state := range []int64{creationPending, creationPublished} {
		for _, session := range []struct{ label, established, pending, expected string }{
			{"established", "current-native-session", "older-pending-session", "current-native-session"},
			{"pending", "", "current-pending-session", "current-pending-session"},
			{"cleared", "", "", ""},
		} {
			t.Run(leapmuxv1.TabCreationState(state).String()+"/"+session.label, func(t *testing.T) {
				t.Parallel()
				hub := newRecoveryPublicationHub(t)
				directory := t.TempDir()
				path := filepath.Join(directory, "worker.db")
				oldDatabase := openRecoveryDatabase(t, path)
				oldService, _ := newRecoveryService(t, hub.config(oldDatabase, directory))
				defer oldService.Shutdown()
				intent := hub.intent("current-agent")
				_, request := recoveryCreationRequest(t, leapmuxv1.TabType_TAB_TYPE_AGENT, intent, directory)
				runtime := &agentCreationRuntime{Startup: creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}}, ResumeSessionID: "stale-runtime-session", Options: OptionMap{agent.OptionIDModel: "stale-model"}, DefaultedIDs: map[string]bool{agent.OptionIDModel: true, "retired-axis": true}}
				seedRecoveryCreation(t, oldService, recoveryCreationSeed{request: request, publication: &leapmuxv1.TabPublication{Creation: intent, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, runtime: runtime, state: state,
					write: func(ctx context.Context, q *db.Queries) error {
						return q.CreateAgent(ctx, db.CreateAgentParams{ID: intent.TabId, WorkingDir: directory, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Options: marshalOptions(runtime.Options), Resumed: 1, PendingResumeSessionID: "stale-runtime-session"})
					},
				})
				current := OptionMap{agent.OptionIDModel: "recovered-custom-model", agent.OptionIDPermissionMode: "bypassPermissions"}
				_, err := oldDatabase.ExecContext(t.Context(), `UPDATE agents SET agent_session_id = ?, pending_resume_session_id = ?, options = ? WHERE id = ?`, session.established, session.pending, marshalOptions(current), intent.TabId)
				require.NoError(t, err)
				oldService.Shutdown()
				require.NoError(t, oldDatabase.Close())
				freshDatabase := openRecoveryDatabase(t, path)
				fresh, _ := newRecoveryService(t, hub.config(freshDatabase, directory))
				defer fresh.Shutdown()
				captured := make(chan agent.Options, 2)
				var starts atomic.Int32
				fresh.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
					starts.Add(1)
					opts.Options = maps.Clone(opts.Options)
					opts.NewSessionDefaultOptionIDs = maps.Clone(opts.NewSessionDefaultOptionIDs)
					select {
					case captured <- opts:
					default:
					}
					return nil, errors.New("the test captures recovered startup")
				}
				ctx := creationOutcomeContext(t)
				require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
				var opts agent.Options
				select {
				case opts = <-captured:
				case <-ctx.Done():
					t.Fatal("recovery did not start the agent")
				}
				fresh.AgentStartup.WaitForInFlight()
				assert.Equal(t, session.expected, opts.ResumeSessionID)
				assert.Equal(t, current[agent.OptionIDModel], opts.Options[agent.OptionIDModel])
				assert.Equal(t, current[agent.OptionIDPermissionMode], opts.Options[agent.OptionIDPermissionMode])
				assert.False(t, opts.NewSessionDefaultOptionIDs[agent.OptionIDModel], "an explicit current model must not retain the old default marker")
				assert.NotContains(t, opts.NewSessionDefaultOptionIDs, "retired-axis")
				require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
				assert.Equal(t, int32(1), starts.Load(), "completed recovery must not launch again")
				row, err := fresh.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: intent.TabId})
				require.NoError(t, err)
				assert.Equal(t, creationCommitted, row.State)
				assert.Zero(t, len(row.RuntimePayload))
			})
		}
	}
}

func TestCreationRestart_RestoresTheCurrentTerminalDimensions(t *testing.T) {
	t.Parallel()
	for _, state := range []int64{creationPending, creationPublished} {
		t.Run(leapmuxv1.TabCreationState(state).String(), func(t *testing.T) {
			t.Parallel()
			hub := newRecoveryPublicationHub(t)
			directory := t.TempDir()
			path := filepath.Join(directory, "worker.db")
			oldDatabase := openRecoveryDatabase(t, path)
			oldService, _ := newRecoveryService(t, hub.config(oldDatabase, directory))
			defer oldService.Shutdown()
			intent := hub.intent("current-terminal")
			_, request := recoveryCreationRequest(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, intent, directory)
			runtime := &terminalCreationRuntime{Startup: creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}}, Options: terminal.Options{ID: intent.TabId, WorkingDir: directory, Shell: testutil.TestShell(), Cols: 80, Rows: 25}, Title: "Saved terminal"}
			seedRecoveryCreation(t, oldService, recoveryCreationSeed{request: request, publication: &leapmuxv1.TabPublication{Creation: intent, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}, runtime: runtime, state: state,
				write: func(ctx context.Context, q *db.Queries) error {
					return q.CreateTerminal(ctx, db.CreateTerminalParams{ID: intent.TabId, WorkingDir: directory, Shell: runtime.Options.Shell, Title: runtime.Title, Cols: 117, Rows: 53, Screen: []byte{}})
				},
			})
			oldService.Shutdown()
			require.NoError(t, oldDatabase.Close())
			freshDatabase := openRecoveryDatabase(t, path)
			fresh, _ := newRecoveryService(t, hub.config(freshDatabase, directory))
			defer fresh.Shutdown()
			captured := make(chan terminal.Options, 2)
			var starts atomic.Int32
			fresh.startTerminalFn = func(_ context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
				starts.Add(1)
				select {
				case captured <- opts:
				default:
				}
				return errors.New("the test captures recovered terminal startup")
			}
			ctx := creationOutcomeContext(t)
			require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
			var opts terminal.Options
			select {
			case opts = <-captured:
			case <-ctx.Done():
				t.Fatal("recovery did not start the terminal")
			}
			fresh.TerminalStartup.WaitForInFlight()
			assert.Equal(t, uint16(117), opts.Cols)
			assert.Equal(t, uint16(53), opts.Rows)
			require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
			assert.Equal(t, int32(1), starts.Load())
			row, err := fresh.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: intent.TabId})
			require.NoError(t, err)
			assert.Equal(t, creationCommitted, row.State)
		})
	}
}
