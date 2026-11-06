package controlipc_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func newRouterService(t *testing.T) (*service.Service, *channel.Dispatcher, *controlipc.Router) {
	t.Helper()
	database, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), database))
	send := func(*leapmuxv1.ConnectRequest) error { return nil }
	// These calls stay inside the local router and never open an encrypted channel.
	channels := channel.NewManager(nil, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, send, nil, 0, 0)
	svc := service.New(service.Config{DB: database, Channels: channels, Send: send,
		Agents: agent.NewManager(providers.Registry(), nil), Terminals: terminal.NewManager(),
		HomeDir: t.TempDir(), DataDir: t.TempDir(), SeedRegisteredBy: "router-owner",
	})
	dispatcher := channel.NewDispatcher()
	service.RegisterAll(dispatcher, svc)
	t.Cleanup(func() {
		dispatcher.CloseAdmission()
		svc.PrivateEvents.Stop()
		svc.Shutdown()
		channels.CloseAll()
		require.True(t, dispatcher.WaitHandlers(30*time.Second), "router fixture handlers did not join")
	})
	return svc, dispatcher, &controlipc.Router{WorkerID: "router-worker", UserID: userid.MustNew("router-owner"), LocalDispatcher: dispatcher, Streams: svc}
}

func TestRouterRealWatchHandlersEndOnParentCancellation(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := "watch events"
		if private {
			name = "private events"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, dispatcher, router := newRouterService(t)
				method := leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure
				var request proto.Message = &leapmuxv1.WatchEventsRequest{UpdateId: 1}
				if private {
					dir := t.TempDir()
					require.NoError(t, svc.TabPayloads.Register(t.Context(), service.RegisterTabPayloadParams{
						UserID: "router-owner", TabID: "snapshot-file", Payload: &leapmuxv1.TabPayload{WorkingDir: dir,
							Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(dir, "file.txt")}},
						},
					}))
					method = leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure
					request = &leapmuxv1.WatchWorkerPrivateEventsRequest{}
				}
				payload, err := proto.Marshal(request)
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				frames := make(chan []byte, 1)
				// The once closure forwards the actual first frame without blocking a send.
				var forward sync.Once
				done := make(chan error, 1)
				go func() {
					done <- router.StreamInner(ctx, controlipc.TokenInfo{UserID: router.UserID, WorkerID: router.WorkerID, TabID: "router-tab"}, "worker."+method, payload, "", "request-cancel", func(frame *leapmuxv1.InnerStreamMessage) error {
						if len(frame.GetPayload()) > 0 {
							forward.Do(func() { frames <- append([]byte(nil), frame.GetPayload()...) })
						}
						return nil
					})
				}()
				frame := <-frames
				if private {
					var event leapmuxv1.WorkerPrivateEvent
					require.NoError(t, proto.Unmarshal(frame, &event))
					require.NotNil(t, event.GetTabPayloadRegistered())
					assert.Equal(t, "snapshot-file", event.GetTabPayloadRegistered().GetTabId())
				} else {
					var event leapmuxv1.WatchEventsResponse
					require.NoError(t, proto.Unmarshal(frame, &event))
					require.NotNil(t, event.GetUpdateAck())
					assert.Equal(t, uint64(1), event.GetUpdateAck().GetUpdateId())
				}
				assert.False(t, dispatcher.WaitHandlers(0), "initial frame must arrive while the real handler remains active")
				cancel()
				synctest.Wait()
				joined := false
				select {
				case err := <-done:
					require.NoError(t, err)
					joined = true
				default:
					t.Error("idle router stream ignored request cancellation")
				}
				assert.True(t, dispatcher.WaitHandlers(0), "request cancellation must join the real handler before bus shutdown")
				if !joined {
					svc.PrivateEvents.Stop()
					require.NoError(t, <-done)
				}
				_, active := router.StreamCancellers.Load("request-cancel")
				assert.False(t, active)
			})
		})
	}
}

func TestRouterRealWatchReplacementPreservesItsNewOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, dispatcher, router := newRouterService(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := func(revision uint64) (<-chan uint64, <-chan error) {
			payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{UpdateId: revision})
			require.NoError(t, err)
			acks := make(chan uint64, 1)
			done := make(chan error, 1)
			go func() {
				done <- router.StreamInner(ctx, controlipc.TokenInfo{UserID: router.UserID, WorkerID: router.WorkerID, TabID: "router-tab"}, "worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, payload, "", "replacement", func(frame *leapmuxv1.InnerStreamMessage) error {
					var event leapmuxv1.WatchEventsResponse
					if err := proto.Unmarshal(frame.GetPayload(), &event); err != nil {
						return err
					}
					if ack := event.GetUpdateAck(); ack != nil {
						acks <- ack.GetUpdateId()
					}
					return nil
				})
			}()
			return acks, done
		}
		firstAck, firstDone := start(1)
		assert.Equal(t, uint64(1), <-firstAck)
		secondAck, secondDone := start(2)
		assert.Equal(t, uint64(2), <-secondAck)
		require.NoError(t, <-firstDone)
		revision, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{UpdateId: 3})
		require.NoError(t, err)
		require.NoError(t, reviseStream(router, "replacement", revision))
		assert.Equal(t, uint64(3), <-secondAck, "old release must preserve the replacement controller")
		_, active := router.StreamCancellers.Load("replacement")
		assert.True(t, active, "old stream cleanup must preserve its replacement")
		cancel()
		require.NoError(t, <-secondDone)
		require.True(t, dispatcher.WaitHandlers(30*time.Second))
		_, active = router.StreamCancellers.Load("replacement")
		assert.False(t, active)
	})
}
