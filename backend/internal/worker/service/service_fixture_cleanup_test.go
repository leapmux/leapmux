package service

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/drain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// testRPCDispatcher lets RPC helpers use either a native or fixture-owned dispatcher.
type testRPCDispatcher interface {
	Methods() []string
	DispatchWith(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter)
	DispatchAsync(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter)
}

type testServiceDispatcher struct {
	*channel.Dispatcher
	mu      sync.Mutex
	closed  bool
	cancels []context.CancelFunc
	streams testStreamOwner
}

func (d *testServiceDispatcher) ownContext(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	if d.closed {
		cancel()
	} else {
		d.cancels = append(d.cancels, cancel)
	}
	d.mu.Unlock()
	return ctx
}

func (d *testServiceDispatcher) DispatchWith(ctx context.Context, caller channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	d.Dispatcher.DispatchWith(d.ownContext(ctx), caller, fixtureCreationRequest(request), &ownedTestResponseWriter{ResponseWriter: writer, owner: &d.streams})
}

func (d *testServiceDispatcher) DispatchAsync(ctx context.Context, caller channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	d.Dispatcher.DispatchAsync(d.ownContext(ctx), caller, fixtureCreationRequest(request), &ownedTestResponseWriter{ResponseWriter: writer, owner: &d.streams})
}

type testStreamOwner struct {
	mu       sync.Mutex
	closed   bool
	bindings map[*testStreamBinding]struct{}
}

type testStreamBinding struct {
	controller channel.StreamController
	release    func()
	done       chan struct{}
}

type ownedTestResponseWriter struct {
	channel.ResponseWriter
	owner *testStreamOwner
}

func (w *ownedTestResponseWriter) BindStream(controller channel.StreamController) (func(), bool) {
	o := w.owner
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if closed {
		return nil, false
	}
	release, ok := w.ResponseWriter.BindStream(controller)
	if !ok {
		return nil, false
	}
	binding := &testStreamBinding{controller: controller, done: make(chan struct{})}
	binding.release = sync.OnceFunc(func() {
		if release != nil {
			release()
		}
		o.mu.Lock()
		delete(o.bindings, binding)
		o.mu.Unlock()
		close(binding.done)
	})
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		controller.OnCancel()
		binding.release()
		return nil, false
	}
	if o.bindings == nil {
		o.bindings = make(map[*testStreamBinding]struct{})
	}
	o.bindings[binding] = struct{}{}
	o.mu.Unlock()
	return binding.release, true
}

func (o *testStreamOwner) close() []*testStreamBinding {
	o.mu.Lock()
	o.closed = true
	bindings := make([]*testStreamBinding, 0, len(o.bindings))
	for binding := range o.bindings {
		bindings = append(bindings, binding)
	}
	o.mu.Unlock()
	for _, binding := range bindings {
		binding.controller.OnCancel()
	}
	return bindings
}

// closeFixture joins owned work while the fixture's database is still open.
func (d *testServiceDispatcher) closeFixture(t *testing.T, svc *Service) {
	t.Helper()
	d.CloseAdmission()
	bindings := d.streams.close()
	d.mu.Lock()
	d.closed = true
	cancels := d.cancels
	d.cancels = nil
	d.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	svc.PrivateEvents.Stop()
	svc.Shutdown()
	svc.Channels.CloseAll()
	require.True(t, d.WaitHandlers(30*time.Second), "fixture RPC handlers did not finish")
	for _, binding := range bindings {
		select {
		case <-binding.done:
		case <-time.After(30 * time.Second):
			t.Fatal("fixture stream release did not finish")
		}
	}
	if tunnels := svc.tunnels.Load(); tunnels != nil {
		tunnels.mu.Lock()
		opening := make([]context.CancelFunc, 0, len(tunnels.opening))
		for id, cancel := range tunnels.opening {
			opening = append(opening, cancel)
			delete(tunnels.opening, id)
		}
		tunnels.mu.Unlock()
		for _, cancel := range opening {
			cancel()
		}
		tunnels.conns.Range(func(key, value any) bool { tunnels.closeAndRemove(key.(string), value.(*tunnelConn)); return true })
		require.True(t, tunnels.readLoops.Wait(30*time.Second, ""), "fixture tunnel read loops did not finish")
		require.True(t, tunnels.contextClosers.Wait(30*time.Second, ""), "fixture tunnel context callbacks did not finish")
		tunnels.Stop()
	}
	svc.Output.rootSinks.Range(func(_, value any) bool { value.(*agentOutputSink).closeProgressTree(); return true })
	require.True(t, svc.Output.progressPublishers.Wait(30*time.Second, ""), "fixture progress publishers did not finish")
}

func newTestProgressPublisher(t *testing.T, publish func(map[string]interface{}), sessionID func() string) *generationProgressPublisher {
	t.Helper()
	var publishers drain.Counter
	publisher := newGenerationProgressPublisher(&publishers, publish, sessionID)
	t.Cleanup(func() {
		publisher.close()
		require.True(t, publishers.Wait(30*time.Second, ""), "test progress publisher did not finish")
	})
	return publisher
}

// submittedTestResponseWriter lets a test start a watch without ending its handler.
type submittedTestResponseWriter struct {
	channel.ResponseWriter
	ready func()
}

func (w *submittedTestResponseWriter) BindStream(controller channel.StreamController) (func(), bool) {
	release, ok := w.ResponseWriter.BindStream(controller)
	if ok {
		w.ready()
	}
	return release, ok
}

func dispatchTestRequest(d testRPCDispatcher, ctx context.Context, caller channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	if request.GetMethod() != leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure {
		d.DispatchWith(ctx, caller, request, writer)
		return
	}
	ready, done := make(chan struct{}), make(chan struct{})
	submitted := &submittedTestResponseWriter{ResponseWriter: writer, ready: sync.OnceFunc(func() { close(ready) })}
	go func() { defer close(done); d.DispatchWith(ctx, caller, request, submitted) }()
	select {
	case <-ready:
	case <-done:
	}
}

type fixtureStreamController struct {
	canceled atomic.Int32
	frames   chan []byte
}

func (c *fixtureStreamController) OnCancel()                    { c.canceled.Add(1) }
func (c *fixtureStreamController) OnClientFrame(payload []byte) { c.frames <- payload }

type fixtureBindWriter struct {
	*testResponseWriter
	bind func(channel.StreamController) (func(), bool)
}

func (w *fixtureBindWriter) BindStream(controller channel.StreamController) (func(), bool) {
	return w.bind(controller)
}

func TestFixtureStreamOwnerDelegatesAndReleasesExactlyOnce(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			var owner testStreamOwner
			var releases atomic.Int32
			controller := &fixtureStreamController{frames: make(chan []byte, 1)}
			delegate := &fixtureBindWriter{testResponseWriter: newTestWriter(), bind: func(ctrl channel.StreamController) (func(), bool) {
				ctrl.OnClientFrame([]byte("initial update"))
				return func() { releases.Add(1) }, accepted
			}}
			writer := &ownedTestResponseWriter{ResponseWriter: delegate, owner: &owner}
			release, ok := writer.BindStream(controller)
			assert.Equal(t, accepted, ok)
			assert.Equal(t, []byte("initial update"), <-controller.frames)
			bindings := owner.close()
			if !accepted {
				assert.Empty(t, bindings)
				assert.Zero(t, releases.Load())
				return
			}
			require.Len(t, bindings, 1)
			assert.Equal(t, int32(1), controller.canceled.Load())
			release()
			release()
			<-bindings[0].done
			assert.Equal(t, int32(1), releases.Load())
			assert.Empty(t, owner.bindings)
		})
	}
}

func TestFixtureStreamOwnerClosesABindingThatRacesShutdown(t *testing.T) {
	var owner testStreamOwner
	entered, proceed := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(proceed) })
	defer finish()
	var releases atomic.Int32
	controller := &fixtureStreamController{}
	writer := &ownedTestResponseWriter{owner: &owner, ResponseWriter: &fixtureBindWriter{
		testResponseWriter: newTestWriter(), bind: func(channel.StreamController) (func(), bool) {
			close(entered)
			<-proceed
			return func() { releases.Add(1) }, true
		},
	}}
	done := make(chan bool, 1)
	go func() { _, ok := writer.BindStream(controller); done <- ok }()
	<-entered
	assert.Empty(t, owner.close())
	finish()
	assert.False(t, <-done)
	assert.Equal(t, int32(1), controller.canceled.Load())
	assert.Equal(t, int32(1), releases.Load())
	assert.Empty(t, owner.bindings)
}

func TestFixtureJoinsEmptyAndSupersededWatchStreams(t *testing.T) {
	svc, dispatcher, _ := setupTestService(t)
	writers := []*testResponseWriter{newTestWriter(), newTestWriter(), newTestWriter()}
	for _, writer := range writers {
		dispatch(dispatcher, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{}, writer)
	}
	// Empty interests leave no entity registration through which cleanup could
	// rediscover these streams. Their exact bindings still belong to the fixture.
	privateWriter := newTestWriter()
	privateDone := make(chan struct{})
	t.Cleanup(func() { svc.PrivateEvents.Stop(); <-privateDone })
	go func() {
		defer close(privateDone)
		dispatch(dispatcher, leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, &leapmuxv1.WatchWorkerPrivateEventsRequest{}, privateWriter)
	}()
	testutil.AssertEventually(t, func() bool {
		svc.PrivateEvents.mu.RLock()
		defer svc.PrivateEvents.mu.RUnlock()
		return len(svc.PrivateEvents.subscribers["user-1"]) == 1
	}, "private stream must be active before fixture cleanup")
	dispatcher.closeFixture(t, svc)
	<-privateDone
	writers = append(writers, privateWriter)
	for _, writer := range writers {
		writer.mu.Lock()
		controller := writer.streamCtrl
		writer.mu.Unlock()
		assert.Nil(t, controller)
	}
	assert.Empty(t, dispatcher.streams.bindings)
	assert.Empty(t, svc.Watchers.channelOwners)
}

func TestFixtureCountsRetiredProgressPublishers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, dispatcher, _ := setupTestService(t)
		const ownerID = "retired-progress-owner"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID: ownerID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		services, err := svc.Output.NewRootSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		require.NoError(t, err)
		value, ok := svc.Output.rootSinks.Load(ownerID)
		require.True(t, ok)
		original := value.(*agentOutputSink)
		childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "child-span", ProviderChildKey: "child", Title: "child"})
		require.NoError(t, err)
		childServices := services.ChildSink(childID)
		descendantID, err := childServices.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "descendant-span", ProviderChildKey: "descendant", Title: "descendant"})
		require.NoError(t, err)
		childServices.ChildSink(descendantID)
		value, ok = svc.Output.sinksByAgent.Load(childID)
		require.True(t, ok)
		child := value.(*agentOutputSink)
		value, ok = svc.Output.sinksByAgent.Load(descendantID)
		require.True(t, ok)
		descendant := value.(*agentOutputSink)

		entered, proceed := make(chan struct{}), make(chan struct{})
		finish := sync.OnceFunc(func() { close(proceed) })
		defer finish()
		announce := sync.OnceFunc(func() { close(entered) })
		descendant.progress.publish = func(map[string]interface{}) {
			announce()
			<-proceed
			_, queryErr := svc.Queries.GetAgentByID(context.Background(), ownerID)
			assert.NoError(t, queryErr, "retired publisher must finish while the database is open")
		}
		descendant.progress.report(agent.ModelTextProgress("model", "abcdefgh"))
		descendant.progress.report(agent.CompleteModelProgress("model"))
		<-entered
		_, err = svc.Output.NewRootSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		require.NoError(t, err)
		value, ok = svc.Output.rootSinks.Load(ownerID)
		require.True(t, ok)
		replacement := value.(*agentOutputSink)
		detached := original.rawChildSink("retired-unregistered-child")
		replacement.closeProgressTree()
		<-replacement.progress.done
		<-original.progress.done
		<-child.progress.done
		<-detached.progress.done
		synctest.Wait()
		assert.False(t, svc.Output.progressPublishers.Wait(0, ""), "a retired publisher callback must remain counted")
		finish()
		require.True(t, svc.Output.progressPublishers.Wait(30*time.Second, ""))
		<-descendant.progress.done
		dispatcher.closeFixture(t, svc)

	})
}

func TestGenerationProgressCloseDoesNotJoinItsOwnCallback(t *testing.T) {
	var lifetimes drain.Counter
	returned := make(chan struct{})
	publisher := newGenerationProgressPublisher(&lifetimes, func(map[string]interface{}) {}, func() string { return "session" })
	defer publisher.close()
	publisher.publish = func(map[string]interface{}) { publisher.close(); close(returned) }
	publisher.report(agent.ModelTextProgress("model", "abcdefgh"))
	publisher.report(agent.CompleteModelProgress("model"))
	<-returned
	require.True(t, lifetimes.Wait(30*time.Second, ""))
	<-publisher.done
}

func TestFixtureClosesTunnelReadersAndContextCallbacks(t *testing.T) {
	svc, dispatcher, _ := setupTestService(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()
	address := listener.Addr().(*net.TCPAddr)
	open := func(id string) net.Conn {
		t.Helper()
		type acceptedConnection struct {
			connection net.Conn
			err        error
		}
		accepted := make(chan acceptedConnection, 1)
		acceptDone := make(chan struct{})
		t.Cleanup(func() {
			<-acceptDone
			select {
			case abandoned := <-accepted:
				if abandoned.connection != nil {
					require.NoError(t, abandoned.connection.Close())
				}
			default:
			}
		})
		go func() {
			defer close(acceptDone)
			connection, acceptErr := listener.Accept()
			accepted <- acceptedConnection{connection, acceptErr}
		}()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure, &leapmuxv1.OpenTunnelConnRequest{
			ConnId: id, TargetAddr: address.IP.String(), TargetPort: uint32(address.Port),
		}, writer)
		require.Empty(t, writer.rejections())
		result := <-accepted
		<-acceptDone
		require.NoError(t, result.err)
		t.Cleanup(func() { require.NoError(t, result.connection.Close()) })
		return result.connection
	}
	open("blocked-read")
	halfClosed := open("half-closed")
	require.NoError(t, halfClosed.(*net.TCPConn).CloseWrite())
	manager := svc.tunnels.Load()
	require.NotNil(t, manager)
	testutil.AssertEventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.halfClosed) == 1 && manager.sweeper.done != nil
	}, "half-close must start the idle reaper")
	manager.mu.Lock()
	sweeperDone := manager.sweeper.done
	manager.mu.Unlock()
	readDone := manager.readLoops.DoneChan()
	contextDone := manager.contextClosers.DoneChan()
	select {
	case <-readDone:
		t.Error("native read finished before fixture cleanup")
	default:
	}
	select {
	case <-contextDone:
		t.Error("context callbacks finished before fixture cleanup")
	default:
	}
	dispatcher.closeFixture(t, svc)
	select {
	case <-readDone:
	default:
		t.Error("fixture did not join tunnel readers")
	}
	select {
	case <-contextDone:
	default:
		t.Error("fixture did not join context callbacks")
	}
	count := 0
	manager.conns.Range(func(_, _ any) bool { count++; return true })
	assert.Zero(t, count)
	manager.mu.Lock()
	assert.Empty(t, manager.opening)
	assert.Empty(t, manager.halfClosed)
	manager.mu.Unlock()
	select {
	case <-sweeperDone:
	default:
		t.Fatal("fixture did not join the idle reaper")
	}
}

func TestFixtureStopsAParkedOutputRetryWithoutAdvancingTime(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	svc, dispatcher, _ := setupTestService(t, withClock(clock))
	var attempts atomic.Int32
	var available atomic.Bool
	completed := make(chan struct{})
	complete := sync.OnceFunc(func() { close(completed) })
	queue := svc.Output.outputQueue("fixture-retry")
	tickets := queue.Append(agent.OutputWrite{Label: "fixture retry", Run: func() error {
		attempts.Add(1)
		if !available.Load() {
			return assert.AnError
		}
		complete()
		return nil
	}})
	timer := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
	t.Cleanup(timer.Close)
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	ctx := testutil.DeadlineContext(t)
	testutil.WaitForTimer(t, ctx, timer)
	before := clock.Now()
	stopTimer := clock.Trap().TimerStop(agent.OutputRetryTimerTag)
	observerCtx, cancelObserver := context.WithCancel(ctx)
	observerDone := make(chan struct{})
	observedStop := make(chan error, 1)
	go func() {
		defer close(observerDone)
		call, stopErr := stopTimer.Wait(observerCtx)
		if stopErr == nil {
			stopErr = call.Release(observerCtx)
		}
		if stopErr != nil {
			stopTimer.Close()
		}
		observedStop <- stopErr
	}()
	defer func() { cancelObserver(); stopTimer.Close(); <-observerDone }()
	available.Store(true)
	dispatcher.closeFixture(t, svc)
	require.NoError(t, <-observedStop, "fixture must cancel the parked retry timer")
	select {
	case <-completed:
	default:
		t.Fatal("fixture did not flush the pending output")
	}
	assert.Equal(t, before, clock.Now(), "cleanup must not wait for a retry interval")
	assert.Equal(t, int32(2), attempts.Load(), "only the original attempt and final flush may run")
	assert.True(t, tickets[0].Done())
	assert.False(t, queue.HasPending())
}

func TestFixtureCleanupReleasesCallbacksBeforeClosingTheDatabase(t *testing.T) {
	var svc *Service
	var publisher *generationProgressPublisher
	completed := make(chan struct{})
	t.Run("owned callback", func(t *testing.T) {
		svc, _, _ = setupTestService(t)
		const ownerID = "cleanup-order-owner"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
		_, err := svc.Output.NewRootSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		require.NoError(t, err)
		value, ok := svc.Output.rootSinks.Load(ownerID)
		require.True(t, ok)
		publisher = value.(*agentOutputSink).progress
		entered, release := make(chan struct{}), make(chan struct{})
		announce := sync.OnceFunc(func() { close(entered) })
		finish := sync.OnceFunc(func() { close(release) })
		complete := sync.OnceFunc(func() { close(completed) })
		t.Cleanup(finish)
		publisher.publish = func(map[string]interface{}) {
			announce()
			<-release
			_, queryErr := svc.Queries.GetAgentByID(context.Background(), ownerID)
			assert.NoError(t, queryErr, "test cleanup must release the callback while the store remains open")
			complete()
		}
		publisher.report(agent.ModelTextProgress("model", "abcdefgh"))
		publisher.report(agent.CompleteModelProgress("model"))
		<-entered
	})
	select {
	case <-completed:
	default:
		t.Fatal("subtest returned before its callback finished")
	}
	select {
	case <-publisher.done:
	default:
		t.Fatal("subtest returned before its publisher finished")
	}
	assert.ErrorContains(t, svc.DB.PingContext(context.Background()), "database is closed")
}

type fatalAssertionRecorder struct{ messages []string }

func (r *fatalAssertionRecorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}
func (*fatalAssertionRecorder) FailNow() { runtime.Goexit() }

func fixturePublishTab(_ context.Context, r *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
	placement := r.GetCreation().GetPlacement()
	tile := placement.GetTileId()
	if tile == "" {
		tile = "fixture-tile"
	}
	return &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Published{Published: &leapmuxv1.PublishedTab{Tab: &leapmuxv1.TabIdent{TabType: r.GetTabType(), TabId: r.GetCreation().GetTabId()}, WorkspaceId: placement.GetWorkspaceId(), TileId: tile, Position: "n"}}}, nil
}
func fixtureCancelTabPublication(context.Context, *leapmuxv1.TabPublication) error { return nil }

// fixtureCreationRequest supplies placement to older resource-focused fixtures.
// Boundary tests use the underlying dispatcher and receive no adaptation.
func fixtureCreationRequest(request *leapmuxv1.InnerRpcRequest) *leapmuxv1.InnerRpcRequest {
	var message proto.Message
	switch request.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
		message = &leapmuxv1.OpenAgentRequest{}
	case leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure:
		message = &leapmuxv1.OpenTerminalRequest{}
	case leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:
		message = &leapmuxv1.RegisterTabPayloadRequest{}
	default:
		return request
	}
	if err := proto.Unmarshal(request.GetPayload(), message); err != nil {
		return request
	}
	var creation *leapmuxv1.TabCreation
	switch r := message.(type) {
	case *leapmuxv1.OpenAgentRequest:
		creation = r.GetCreation()
		if creation == nil {
			creation = &leapmuxv1.TabCreation{}
			r.Creation = creation
		}
	case *leapmuxv1.OpenTerminalRequest:
		if r.GetQuake() != nil {
			return request
		}
		creation = r.GetCreation()
		if creation == nil {
			creation = &leapmuxv1.TabCreation{}
			r.Target = &leapmuxv1.OpenTerminalRequest_Creation{Creation: creation}
		}
	case *leapmuxv1.RegisterTabPayloadRequest:
		creation = r.GetCreation()
		if creation == nil {
			creation = &leapmuxv1.TabCreation{}
			r.Creation = creation
		}
	}
	if creation.GetTabId() == "" {
		creation.TabId = id.Generate()
	}
	if creation.GetPlacement() == nil {
		creation.Placement = &leapmuxv1.TabPlacement{WorkspaceId: "fixture-workspace", Destination: &leapmuxv1.TabPlacement_TileId{TileId: "fixture-tile"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}
	}
	encoded, err := proto.Marshal(message)
	if err != nil {
		panic(err)
	}
	captured := proto.CloneOf(request)
	captured.Payload = encoded
	return captured
}

func fixtureResolveTabPublication(ctx context.Context, r *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
	receipt, err := fixturePublishTab(ctx, r)
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.ResolveTabPublicationResponse{Receipt: receipt, Presence: leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED, ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE}, nil
}
