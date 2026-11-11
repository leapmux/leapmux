package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func creationIntent(tabID string) *leapmuxv1.TabCreation {
	return &leapmuxv1.TabCreation{TabId: tabID, Placement: &leapmuxv1.TabPlacement{WorkspaceId: "workspace", Destination: &leapmuxv1.TabPlacement_TileId{TileId: "tile"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}
}
func TestTabCreation_PreservesEveryResourceWhilePublicationWaits(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			svc, dispatcher, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
				return nil, errors.New("test prevents agent startup")
			}
			svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
				return errors.New("test prevents terminal startup")
			}
			entered := make(chan struct{})
			var enteredOnce sync.Once
			release := make(chan struct{})
			svc.PublishTab = func(ctx context.Context, r *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
				require.Equal(t, "pending", r.GetCreation().GetTabId())
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-release:
					return fixturePublishTab(ctx, r)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			workingDir := t.TempDir()
			method, request := creationRequestForTest(t, kind, "pending", workingDir)
			writer := newTestWriter()
			done := make(chan struct{})
			go func() { defer close(done); dispatch(dispatcher, method, request, writer) }()
			released := false
			defer func() {
				if !released {
					close(release)
				}
				<-done
			}()
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("creation never reached Hub publication")
			}
			assert.Empty(t, writer.responsesSnapshot(), "unary success requires a Hub commit")
			clock := time.Now()
			reconciler := NewOrphanReconciler(svc.Queries, func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
				return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}, nil
			}, OrphanReconcilerOptions{Now: func() time.Time { return clock }, BeginTabReconciliation: svc.BeginTabReconciliation, ResumeAgents: ignoreAgentResume})
			require.False(t, reconciler.reconcileOnce(t.Context()))
			clock = clock.Add(11 * time.Second)
			require.False(t, reconciler.reconcileOnce(t.Context()), "a protected creation prevents convergence")
			switch kind {
			case leapmuxv1.TabType_TAB_TYPE_AGENT:
				row, err := svc.Queries.GetAgentByID(t.Context(), "pending")
				require.NoError(t, err)
				assert.False(t, row.ClosedAt.Valid)
			case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
				row, err := svc.Queries.GetTerminal(t.Context(), "pending")
				require.NoError(t, err)
				assert.False(t, row.ClosedAt.Valid)
			default:
				_, err := svc.TabPayloads.Get(t.Context(), "user-1", "pending")
				require.NoError(t, err)
			}
			close(release)
			released = true
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("creation did not return after Hub publication")
			}
			require.Empty(t, writer.rejections())
			require.Len(t, writer.responsesSnapshot(), 1)
			repeated := newTestWriter()
			dispatch(dispatcher, method, request, repeated)
			require.Empty(t, repeated.rejections())
			require.Len(t, repeated.responsesSnapshot(), 1)
		})
	}
}

func TestTabCreation_PreservesTerminalResizeAcrossPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, dispatcher, _ := setupTestService(t)
		defer drainAllInFlight(svc)
		publicationEntered := make(chan struct{})
		releasePublication := make(chan struct{})
		svc.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
			close(publicationEntered)
			select {
			case <-releasePublication:
				return fixturePublishTab(ctx, request)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		started := make(chan terminal.Options, 1)
		svc.startTerminalFn = func(_ context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
			started <- opts
			return errors.New("test prevents terminal startup")
		}
		writer := newTestWriter()
		openDone := make(chan struct{})
		go func() {
			defer close(openDone)
			dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{Target: &leapmuxv1.OpenTerminalRequest_Creation{Creation: creationIntent("resize")}, WorkingDir: t.TempDir(), Cols: 80, Rows: 25}, writer)
		}()
		released := false
		defer func() {
			if !released {
				close(releasePublication)
			}
			<-openDone
		}()
		<-publicationEntered
		resize := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, &leapmuxv1.ResizeTerminalRequest{TerminalId: "resize", Cols: 117, Rows: 53}, resize)
		require.Empty(t, resize.rejections())
		close(releasePublication)
		released = true
		<-openDone
		require.Empty(t, writer.rejections())
		opts := <-started
		assert.Equal(t, uint16(117), opts.Cols, "startup must use the resize accepted during publication")
		assert.Equal(t, uint16(53), opts.Rows, "startup must retain both accepted dimensions")
	})
}

func testProcessCreation(kind leapmuxv1.TabType, id, workingDir string) (string, proto.Message) {
	if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{Creation: creationIntent(id), WorkingDir: workingDir}
	}
	return leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{Target: &leapmuxv1.OpenTerminalRequest_Creation{Creation: creationIntent(id)}, WorkingDir: workingDir, Cols: 80, Rows: 25}
}

func TestTabCreation_RetainsRecoveryUntilStartupFailureIsDurable(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, _ := setupTestService(t)
				table := "agents"
				if kind == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
					table = "terminals"
				}
				_, err := svc.DB.ExecContext(t.Context(), "CREATE TRIGGER refuse_startup_error BEFORE UPDATE OF startup_error ON "+table+" WHEN NEW.startup_error <> '' BEGIN SELECT RAISE(FAIL, 'test refuses startup error writes'); END")
				require.NoError(t, err)
				defer func() {
					_, _ = svc.DB.ExecContext(t.Context(), "DROP TRIGGER IF EXISTS refuse_startup_error")
					drainAllInFlight(svc)
				}()
				attempted := make(chan struct{})
				svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
					close(attempted)
					return nil, errors.New("provider startup failed")
				}
				svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
					close(attempted)
					return errors.New("shell startup failed")
				}
				method, request := testProcessCreation(kind, "failure", t.TempDir())
				writer := newTestWriter()
				dispatch(dispatcher, method, request, writer)
				require.Empty(t, writer.rejections())
				<-attempted
				synctest.Wait()
				key := db.GetTabCreationParams{UserID: "user-1", WorkerID: "worker-1", TabID: "failure"}
				row, err := svc.Queries.GetTabCreation(t.Context(), key)
				require.NoError(t, err)
				assert.Equal(t, creationPublished, row.State, "completion must wait for a durable startup outcome")
				assert.NotEmpty(t, row.RuntimePayload, "failed persistence must retain recovery inputs")
				_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_startup_error")
				require.NoError(t, err)
				drainAllInFlight(svc)
				row, err = svc.Queries.GetTabCreation(t.Context(), key)
				require.NoError(t, err)
				assert.Equal(t, creationCommitted, row.State)
				assert.Empty(t, row.RuntimePayload)
				if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
					resource, err := svc.Queries.GetAgentByID(t.Context(), "failure")
					require.NoError(t, err)
					assert.Equal(t, "provider startup failed", resource.StartupError)
				} else {
					resource, err := svc.Queries.GetTerminal(t.Context(), "failure")
					require.NoError(t, err)
					assert.Equal(t, "shell startup failed", resource.StartupError)
				}
			})
		})
	}
}

func TestTabCreation_DoesNotApplyStaleActiveArchiveObservation(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, _ := setupTestService(t)
				defer drainAllInFlight(svc)
				var archived atomic.Bool
				var first atomic.Bool
				var starts atomic.Int32
				entered := make(chan struct{})
				release := make(chan struct{})
				svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
					observedArchived := archived.Load()
					if first.CompareAndSwap(false, true) {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					result, err := fixtureResolveTabPublication(ctx, request)
					if observedArchived {
						result.ArchiveState = leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED
					}
					return result, err
				}
				svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
					starts.Add(1)
					return nil, errors.New("test prevents agent startup")
				}
				svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
					starts.Add(1)
					return errors.New("test prevents shell startup")
				}
				method, request := testProcessCreation(kind, "archived", t.TempDir())
				writer := newTestWriter()
				done := make(chan struct{})
				go func() { defer close(done); dispatch(dispatcher, method, request, writer) }()
				released := false
				defer func() {
					if !released {
						close(release)
					}
					<-done
				}()
				<-entered
				_, err := svc.ApplyTabArchiveState(t.Context(), leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, userid.MustNew("user-1"), []*leapmuxv1.TabIdent{{TabType: kind, TabId: "archived"}})
				require.NoError(t, err)
				archived.Store(true)
				close(release)
				released = true
				<-done
				require.Empty(t, writer.rejections())
				synctest.Wait()
				assert.Zero(t, starts.Load(), "a delayed ACTIVE response must not start an archived resource")
				if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
					row, err := svc.Queries.GetAgentByID(t.Context(), "archived")
					require.NoError(t, err)
					assert.True(t, row.WorkspaceArchived)
				} else {
					row, err := svc.Queries.GetTerminal(t.Context(), "archived")
					require.NoError(t, err)
					assert.True(t, row.WorkspaceArchived)
				}
			})
		})
	}
}

func TestTabCreation_FinishesRecoveryForClosedPublishedResource(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, _ := setupTestService(t)
				svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
					result, err := fixtureResolveTabPublication(ctx, request)
					result.ArchiveState = leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED
					return result, err
				}
				var starts atomic.Int32
				svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
					starts.Add(1)
					return nil, errors.New("test prevents startup")
				}
				svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
					starts.Add(1)
					return errors.New("test prevents startup")
				}
				method, request := testProcessCreation(kind, "closed", t.TempDir())
				writer := newTestWriter()
				dispatch(dispatcher, method, request, writer)
				require.Empty(t, writer.rejections())
				svc.CloseTabForReconcile(kind, "", "closed")
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				err := svc.ResolvePendingTabCreations(ctx)
				assert.NoError(t, err, "a durably closed resource is resolved recovery work")
				row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: "worker-1", TabID: "closed"})
				require.NoError(t, err)
				assert.Equal(t, creationCanceled, row.State)
				assert.Empty(t, row.RuntimePayload)
				assert.Empty(t, row.RequestPayload)
				assert.Zero(t, starts.Load())
			})
		})
	}
}

func TestTabCreation_WaitsForDurableCheckpointBeforeReplacement(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, _ := setupTestService(t)
				_, err := svc.DB.ExecContext(t.Context(), "CREATE TRIGGER hold_creation_completion BEFORE UPDATE OF state ON tab_creations WHEN NEW.state = 2 BEGIN SELECT RAISE(FAIL, 'test holds creation checkpoint'); END")
				require.NoError(t, err)
				defer func() {
					_, _ = svc.DB.ExecContext(t.Context(), "DROP TRIGGER IF EXISTS hold_creation_completion")
					drainAllInFlight(svc)
				}()
				attempted := make(chan struct{})
				svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
					close(attempted)
					return nil, errors.New("provider startup failed")
				}
				svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
					close(attempted)
					return errors.New("shell startup failed")
				}
				method, request := testProcessCreation(kind, "checkpoint", t.TempDir())
				writer := newTestWriter()
				dispatch(dispatcher, method, request, writer)
				require.Empty(t, writer.rejections())
				<-attempted
				synctest.Wait()
				row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: "worker-1", TabID: "checkpoint"})
				require.NoError(t, err)
				require.Equal(t, creationPublished, row.State)
				waiter := make(chan error, 1)
				go func() { waiter <- svc.awaitCreationStartup(t.Context(), kind, "checkpoint") }()
				synctest.Wait()
				returnedEarly := false
				select {
				case err := <-waiter:
					returnedEarly = true
					assert.Fail(t, "replacement passed an unfinished creation checkpoint", "returned error: %v", err)
				default:
				}
				_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER hold_creation_completion")
				require.NoError(t, err)
				drainAllInFlight(svc)
				if !returnedEarly {
					require.NoError(t, <-waiter)
				}
			})
		})
	}
}

func TestTabCreation_ReportsStartingWhileLifecycleResolutionWaits(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, _ := setupTestService(t)
				entered := make(chan struct{})
				release := make(chan struct{})
				svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
					close(entered)
					select {
					case <-release:
						return fixtureResolveTabPublication(ctx, request)
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
					return nil, errors.New("test prevents startup")
				}
				svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
					return errors.New("test prevents startup")
				}
				method, request := testProcessCreation(kind, "status", t.TempDir())
				writer := newTestWriter()
				done := make(chan struct{})
				go func() { defer close(done); dispatch(dispatcher, method, request, writer) }()
				defer func() { close(release); <-done; drainAllInFlight(svc) }()
				<-entered
				listed := newTestWriter()
				if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
					dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{"status"}}, listed)
					require.Empty(t, listed.rejections())
					require.Len(t, listed.responsesSnapshot(), 1)
					response := &leapmuxv1.ListAgentsResponse{}
					require.NoError(t, proto.Unmarshal(listed.responsesSnapshot()[0].GetPayload(), response))
					require.Len(t, response.GetAgents(), 1)
					assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, response.GetAgents()[0].GetStatus())
				} else {
					dispatch(dispatcher, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: []string{"status"}}, listed)
					require.Empty(t, listed.rejections())
					require.Len(t, listed.responsesSnapshot(), 1)
					response := &leapmuxv1.ListTerminalsResponse{}
					require.NoError(t, proto.Unmarshal(listed.responsesSnapshot()[0].GetPayload(), response))
					require.Len(t, response.GetTerminals(), 1)
					assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, response.GetTerminals()[0].GetStatus())
				}
			})
		})
	}
}

func TestTabCreation_AnnouncesGitSetupPhaseBeforeExecution(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			plan := gitModePlan{Mode: gitModeCreateBranch, WorkingDir: t.TempDir(), BranchName: "feature"}
			caller := channel.NewCaller(userid.MustNew("user-1"), authscope.NonAdminGrant())
			op := &creationOperation{coordinator: svc.creations, caller: caller, publication: &leapmuxv1.TabPublication{Creation: creationIntent("phase"), TabType: kind}}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
				op.runtime = &agentCreationRuntime{Startup: creationStartupIntent{Plan: plan}}
				handle := svc.AgentStartup.begin("phase", func() {})
				require.NotNil(t, handle)
				defer svc.AgentStartup.abandon(handle)
				require.True(t, svc.AgentStartup.bindCreation(handle, op))
				_, err := svc.runAgentPhase0(ctx, &db.Agent{ID: "phase", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}, plan, handle)
				require.ErrorIs(t, err, context.Canceled)
				_, _, message, _ := svc.AgentStartup.status("phase")
				assert.Equal(t, "Creating branch \"feature\"…", message)
			} else {
				op.runtime = &terminalCreationRuntime{Startup: creationStartupIntent{Plan: plan}}
				handle := svc.TerminalStartup.begin("phase", func() {})
				require.NotNil(t, handle)
				defer svc.TerminalStartup.abandon(handle)
				require.True(t, svc.TerminalStartup.bindCreation(handle, op))
				_, err := svc.runTerminalPhase0(ctx, "phase", plan, handle)
				require.ErrorIs(t, err, context.Canceled)
				_, _, message, _ := svc.TerminalStartup.status("phase")
				assert.Equal(t, "Creating branch \"feature\"…", message)
			}
		})
	}
}
