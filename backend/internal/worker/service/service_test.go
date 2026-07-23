package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "failed to get home dir")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/Documents", filepath.Join(home, "Documents")},
		{"tilde nested", "~/a/b/c", filepath.Join(home, "a/b/c")},
		{"absolute path unchanged", "/usr/local/bin", "/usr/local/bin"},
		{"relative path unchanged", "some/path", "some/path"},
		{"empty string", "", ""},
		{"double tilde unchanged", "~~", "~~"},
		{"tilde in middle unchanged", "/foo/~/bar", "/foo/~/bar"},
		{"tilde user unchanged", "~user/foo", "~user/foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTilde(tt.in)
			assert.Equal(t, tt.want, got, "expandTilde(%q)", tt.in)
		})
	}
}

// dispatchOwnerOnlyProbe runs one request through a handler registered the way the
// machine-scoped families are (ownerOnlyRegistrar), and reports whether the gate
// admitted the caller. It exercises the REAL gate rather than calling
// requireWorkerOwner directly, so an owner the Hub clobbered is observed exactly as
// a file/git/tunnel RPC would observe it.
func dispatchOwnerOnlyProbe(svc *Service, userID string) bool {
	d := channel.NewDispatcher()
	admitted := false
	ownerOnlyRegistrar{r: newRegistrar(d, svc)}.Register("Probe",
		func(context.Context, string, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
			admitted = true
		})
	d.DispatchWith(context.Background(), userID, &leapmuxv1.InnerRpcRequest{Method: "Probe"}, newTestWriter())
	return admitted
}

// An empty owner from the Hub must NOT clobber a good one.
//
// requireWorkerOwner refuses an empty owner, so storing "" would make the worker
// deny every machine-scoped RPC to its own legitimate user until the next
// connect -- indistinguishably from a real cross-tenant refusal, which is the
// exact failure the Hub-pushed owner exists to prevent. Keeping the previous
// owner is the only safe direction: the Hub cannot legitimately un-own a live
// worker (that is what deregistration is for).
func TestUpdateRegisteredByIgnoresEmptyOwner(t *testing.T) {
	svc := &Service{}
	svc.SetRegisteredBy("user-1")

	svc.UpdateRegisteredBy("")

	assert.Equal(t, "user-1", svc.RegisteredBy(), "an empty push must not clobber the owner")
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-1"),
		"the worker must still serve its own owner after an empty push")
}

// ...and the guard must not be so broad that it pins the first owner forever: a
// genuine re-registration under a different user is the Hub's call, and the worker
// converges on it.
func TestUpdateRegisteredByAppliesOwnerChange(t *testing.T) {
	svc := &Service{}
	svc.SetRegisteredBy("user-1")

	// The drift path (prev != "" && prev != new) warns and STILL stores: the Hub is
	// the authority, so the warning is a breadcrumb, never a veto.
	svc.UpdateRegisteredBy("user-2")

	assert.Equal(t, "user-2", svc.RegisteredBy())
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-2"), "the new owner is served")
	assert.False(t, dispatchOwnerOnlyProbe(svc, "user-1"), "the previous owner is not")
}

// The first delivery on a worker with no seed populates the owner.
func TestUpdateRegisteredByAppliesFirstOwner(t *testing.T) {
	svc := &Service{}
	require.Empty(t, svc.RegisteredBy(), "no owner before the Hub delivers one")

	svc.UpdateRegisteredBy("user-1")

	assert.Equal(t, "user-1", svc.RegisteredBy())
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-1"))
}

// TestLazyProtoJSON_RendersTheSameTextAsProtojson guards the wrapper
// against silently changing the log format it replaced.
func TestLazyProtoJSON_RendersTheSameTextAsProtojson(t *testing.T) {
	msg := &leapmuxv1.AgentEvent{AgentId: "agent-1"}
	assert.Equal(t, protojson.Format(msg), lazyProtoJSON(msg).LogValue().String())
}

// TestLazyProtoJSON_PayloadReachesAnEnabledHandler pins that deferring
// the render costs nothing in fidelity: an enabled record still carries
// the formatted payload.
func TestLazyProtoJSON_PayloadReachesAnEnabledHandler(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logger.Debug("stream payload", "payload", lazyProtoJSON(&leapmuxv1.AgentEvent{AgentId: "agent-lazy"}))

	assert.Contains(t, buf.String(), "agent-lazy",
		"an enabled Debug record must still carry the rendered payload")
}

// TestNoEagerProtojsonFormatInLogCalls is the guard that actually keeps
// the hot paths cheap.
//
// slog evaluates its arguments eagerly, so `slog.Debug("x", "payload",
// protojson.Format(msg))` runs a full reflection-driven proto->JSON
// render on EVERY call and discards it at the default INFO level. A
// benchmark put that at roughly three quarters of the time and nine
// tenths of the bytes of a broadcast. The failure is silent -- the logs
// stay correct, only the CPU is wasted -- so no behavioural assertion
// catches a regression. Scanning the source does.
//
// protojson.Format belongs in exactly one place: protoJSONValue.LogValue,
// which slog calls only once a record survives the level check.
//
// The walk covers the whole worker tree, not just this package. The
// hazard is wherever a proto meets a log call, and the packages next
// door -- channel, remoteipc, terminal -- are on the same hot paths; a
// package-local scan would let the pattern reappear one directory over
// while still reading as if it were enforced everywhere.
func TestNoEagerProtojsonFormatInLogCalls(t *testing.T) {
	// This test lives in internal/worker/service; walk from the worker
	// tree root so every sibling package is covered.
	root := filepath.Join("..", "..", "worker")

	var scanned int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Generated code is not hand-written and not ours to police.
			if entry.Name() == "generated" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "protojson.Format(") {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			// The single sanctioned call, inside the deferred LogValue.
			if strings.Contains(line, "return slog.StringValue(protojson.Format(p.msg))") {
				continue
			}
			t.Errorf("%s:%d calls protojson.Format eagerly; wrap the message in lazyProtoJSON instead:\n\t%s",
				path, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 50, "the walk must actually reach the worker tree")
}

// TestNew_CarriesEveryConfigField pins that Config is wired through
// rather than partially applied. New has no coverage from the two
// production entry points (cmd/leapmux's runWorker has no Go test at
// all), so a field silently dropped here would only surface as a
// mysterious runtime default.
//
// Service embeds Config, so the copy is structural and cannot drop a
// field. What this test still buys is the other half: it walks Config by
// reflection and fails if the literal below leaves any field zero, so
// adding a field to Config forces someone to decide what it means here
// instead of it arriving untested.
func TestNew_CarriesEveryConfigField(t *testing.T) {
	sqlDB := newServiceTestDB(t)

	cfg := Config{
		Channels:            channel.NewManager(nil, 0, nil, 0),
		Send:                func(*leapmuxv1.ConnectRequest) error { return nil },
		DB:                  sqlDB,
		Agents:              agent.NewManager(nil),
		Terminals:           terminal.NewManager(),
		HomeDir:             "/home/x",
		DataDir:             "/data/x",
		WorkerID:            "worker-1",
		Name:                "display-name",
		SeedRegisteredBy:    "user-1",
		AgentStartupTimeout: 11 * time.Second,
		APITimeout:          7 * time.Second,
		UseLoginShell:       true,
		WakeLock:            wakelock.NewActivityTracker(),
	}

	v := reflect.ValueOf(cfg)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		assert.Falsef(t, v.Field(i).IsZero(),
			"Config.%s is zero in this test; every field must be exercised so a "+
				"newly added one cannot reach production untested", name)
	}

	svc := New(cfg)

	assert.Same(t, sqlDB, svc.DB)
	assert.Same(t, cfg.Agents, svc.Agents)
	assert.Same(t, cfg.Terminals, svc.Terminals)
	assert.Same(t, cfg.Channels, svc.Channels)
	assert.Same(t, cfg.WakeLock, svc.WakeLock)
	assert.Equal(t, "/home/x", svc.HomeDir)
	assert.Equal(t, "/data/x", svc.DataDir)
	assert.Equal(t, "worker-1", svc.WorkerID)
	assert.Equal(t, "display-name", svc.Name)
	assert.Equal(t, 11*time.Second, svc.AgentStartupTimeout)
	assert.Equal(t, 7*time.Second, svc.APITimeout)
	assert.True(t, svc.UseLoginShell)
	assert.NotNil(t, svc.Send, "Send must be carried over")

	// The one field New still translates by hand: the seed becomes the
	// atomic the Hub later overwrites.
	assert.Equal(t, "user-1", svc.RegisteredBy(), "SeedRegisteredBy seeds the owner")

	// Derived state New is responsible for building.
	assert.NotNil(t, svc.Queries)
	assert.NotNil(t, svc.Watchers)
	assert.NotNil(t, svc.Output)
	assert.NotNil(t, svc.AgentStartup)
	assert.NotNil(t, svc.TerminalStartup)
	assert.NotNil(t, svc.PrivateEvents)
	assert.NotNil(t, svc.FileTabPaths)
	assert.Equal(t, "/data/x", svc.Output.DataDir, "Output inherits the data dir")
}

// TestNew_EmptyRegisteredByLeavesOwnerUnset pins that omitting the field
// means "the Hub is the authority", not "the owner is the empty string"
// -- requireWorkerOwner refuses "" outright, so this must fail closed.
func TestNew_EmptyRegisteredByLeavesOwnerUnset(t *testing.T) {
	sqlDB := newServiceTestDB(t)

	svc := newMinimalService(t, sqlDB)

	assert.Equal(t, "", svc.RegisteredBy())
}

// TestNew_PanicsOnMissingRequiredConfig pins the backstop for an empty
// or partially-filled Config: a Service that cannot answer an RPC must
// fail at the line that built it, not on the first request. Validating in
// New rather than a follow-up Init leaves no second call to forget.
func TestNew_PanicsOnMissingRequiredConfig(t *testing.T) {
	sqlDB := newServiceTestDB(t)

	t.Run("missing Channels", func(t *testing.T) {
		assert.PanicsWithValue(t, "service.New: Channels must be set", func() {
			New(Config{DB: sqlDB, Send: func(*leapmuxv1.ConnectRequest) error { return nil }})
		})
	})

	t.Run("missing Send", func(t *testing.T) {
		assert.PanicsWithValue(t, "service.New: Send must be set", func() {
			New(Config{DB: sqlDB, Channels: channel.NewManager(nil, 0, nil, 0)})
		})
	})
}

// newServiceTestDB opens a migrated in-memory worker DB for the tests
// above, which need a real *sql.DB but none of setupTestService's
// dispatcher/channel scaffolding.
func newServiceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))
	return sqlDB
}

// TestRegisterAll_BindsTheCleanupDrain pins the invariant that replaced
// a forgotten wiring call. RegisterAll takes both the dispatcher and the
// service, so it binds svc.Cleanup itself: there is no second call an
// entry point can omit, and one did -- shipping a worker whose Shutdown
// waited on an always-zero WaitGroup while tracked close handlers were
// still mutating the DB teardown was about to close.
//
// Asserted positively, through a real tracked dispatch, rather than by
// checking a flag: what matters is that Wait actually blocks.
func TestRegisterAll_BindsTheCleanupDrain(t *testing.T) {
	sqlDB := newServiceTestDB(t)
	svc := newMinimalService(t, sqlDB)

	d := channel.NewDispatcher()
	RegisterAll(d, svc)

	release := make(chan struct{})
	entered := make(chan struct{})
	d.RegisterTracked("test.Tracked", func(context.Context, string, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
		close(entered)
		<-release
	})

	d.DispatchAsync(context.Background(), "user-1",
		&leapmuxv1.InnerRpcRequest{Method: "test.Tracked"}, newTestWriter())

	<-entered
	waited := make(chan struct{})
	go func() { defer close(waited); svc.Cleanup.Wait() }()

	select {
	case <-waited:
		t.Fatal("Cleanup.Wait returned while a tracked handler was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Cleanup.Wait never returned after the handler finished")
	}
}

// TestAuthorizerFor_RoutesLocalIPCToTheRegistry pins that a synthetic
// local-IPC stream id resolves through the per-Service registry rather
// than the channel manager, which knows nothing about it.
func TestAuthorizerFor_RoutesLocalIPCToTheRegistry(t *testing.T) {
	sqlDB := newServiceTestDB(t)
	svc := newMinimalService(t, sqlDB)

	streamID := LocalIPCStreamPrefix + "token:abc"
	svc.RegisterLocalAuthorizer(streamID, []string{"ws-1"})
	defer svc.ReleaseLocalStream(streamID)

	auth := svc.AuthorizerFor(streamID)

	assert.True(t, auth.IsAccessible("ws-1"), "registered workspace is accessible")
	assert.False(t, auth.IsAccessible("ws-2"), "unregistered workspace is not")
}

// TestAuthorizerFor_UnregisteredLocalIPCFailsClosed pins that a missing
// registration denies rather than falling back to the channel manager,
// which would answer for a channel id that does not exist.
func TestAuthorizerFor_UnregisteredLocalIPCFailsClosed(t *testing.T) {
	sqlDB := newServiceTestDB(t)
	svc := newMinimalService(t, sqlDB)

	auth := svc.AuthorizerFor(LocalIPCStreamPrefix + "token:missing")

	assert.False(t, auth.IsAccessible("ws-1"), "an unregistered stream must be denied")
	assert.Empty(t, auth.AccessibleSet())
}

// newMinimalService builds the smallest Service New will accept: a real
// DB plus the two fields it refuses to construct without.
//
// The stand-ins are deliberate. Channels is an empty manager, so
// AuthorizerFor resolves to a channel authorizer that grants nothing —
// which is what a test about local-IPC routing or handler registration
// wants. Send discards, because none of those tests reads the Hub.
func newMinimalService(t *testing.T, sqlDB *sql.DB) *Service {
	t.Helper()
	return New(Config{
		DB:       sqlDB,
		Channels: channel.NewManager(nil, 0, nil, 0),
		Send:     func(*leapmuxv1.ConnectRequest) error { return nil },
	})
}

// TestNew_WiresTheOutputHandlerSeams pins the wiring that used to live in
// a separate Init step.
//
// Both seams fail silently if dropped: without sendMessageFunc an
// auto-continue schedule fires and injects nothing, so an agent that
// should have been nudged just stops; without agentStartingFunc,
// PersistSettingsRefresh cannot tell it is inside the startup window and
// clobbers a settings change made mid-startup. Neither shows up as an
// error anywhere, which is exactly why folding them into the constructor
// needs a guard.
func TestNew_WiresTheOutputHandlerSeams(t *testing.T) {
	svc := newMinimalService(t, newServiceTestDB(t))

	assert.NotNil(t, svc.Output.sendMessageFunc,
		"auto-continue must be able to inject synthetic user messages")
	assert.NotNil(t, svc.Output.agentStarting,
		"PersistSettingsRefresh must be able to detect the startup window")
}

// TestConfigFieldsDoNotShadowServiceMethods makes the embedding hazard
// mechanical instead of remembered.
//
// Service embeds Config, so a Config field and a *Service method share one
// namespace. Most collisions are caught by the compiler, but one is not:
// when a caller sets Config.Foo while every reader calls a same-named
// accessor, the configured value is simply ignored and nothing fails.
// That is exactly why the owner seed had to be named SeedRegisteredBy --
// RegisteredBy() already existed. Today that rename is enforced only by a
// comment, which the next field to collide will not read.
func TestConfigFieldsDoNotShadowServiceMethods(t *testing.T) {
	methods := make(map[string]struct{})
	svcType := reflect.TypeOf(&Service{})
	for i := range svcType.NumMethod() {
		methods[svcType.Method(i).Name] = struct{}{}
	}

	cfgType := reflect.TypeOf(Config{})
	for i := range cfgType.NumField() {
		name := cfgType.Field(i).Name
		_, collides := methods[name]
		assert.False(t, collides,
			"Config.%s collides with (*Service).%s: the promoted field and the method "+
				"share a name, so a caller setting the field while readers call the "+
				"method loses the value silently. Rename the field (see SeedRegisteredBy).",
			name, name)
	}
}

// rejectingWriter refuses SendResponse the way the channel does when a
// payload is over the size cap, and records what came next.
type rejectingWriter struct {
	testResponseWriter
	reject bool
}

func (w *rejectingWriter) SendResponse(r *leapmuxv1.InnerRpcResponse) error {
	if w.reject {
		return fmt.Errorf("message too large: %w", channel.ErrMessageRejected)
	}
	return w.testResponseWriter.SendResponse(r)
}

// TestSendProtoResponse_AnswersWhenTheChannelRefusesTheReply covers the
// unary half of the size cap.
//
// A rejected message means the channel refused THIS payload on its own
// terms -- the transport is fine, so a small reply still gets through.
// Discarding the error left the caller with nothing at all on the wire,
// waiting out its request timeout, and every oversize response looked
// exactly like a hung worker.
func TestSendProtoResponse_AnswersWhenTheChannelRefusesTheReply(t *testing.T) {
	t.Run("a rejected reply becomes RESOURCE_EXHAUSTED", func(t *testing.T) {
		w := &rejectingWriter{testResponseWriter: testResponseWriter{channelID: testChannelID}, reject: true}

		sendProtoResponse(w, &leapmuxv1.ReadFileResponse{Path: "/tmp/x"})

		require.Len(t, w.errors, 1, "the caller must be told something")
		assert.Equal(t, int32(codes.ResourceExhausted), w.errors[0].code)
		assert.Empty(t, w.responses, "the oversize payload itself must not be retried")
	})

	t.Run("an accepted reply sends nothing extra", func(t *testing.T) {
		w := &rejectingWriter{testResponseWriter: testResponseWriter{channelID: testChannelID}}

		sendProtoResponse(w, &leapmuxv1.ReadFileResponse{Path: "/tmp/x"})

		require.Len(t, w.responses, 1)
		assert.Empty(t, w.errors, "a successful send must not also report an error")
	})
}
