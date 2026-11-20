package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type pausedSemanticWatchWriter struct {
	*testResponseWriter
	sentFirst chan struct{}
	resume    chan struct{}
}

func (w *pausedSemanticWatchWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	if err := w.testResponseWriter.SendStream(message); err != nil {
		return err
	}
	var frame leapmuxv1.WatchSemanticEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &frame); err == nil && frame.GetRecord().GetSequence() == 1 {
		close(w.sentFirst)
		<-w.resume
	}
	return nil
}

func TestWatchSemanticEventsRevokesAnOpenHistoryPage(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(svc.Shutdown)
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)

	const terminalID = "terminal-with-history"
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: terminalID, WorkingDir: t.TempDir(), Cols: 200, Rows: 24, Screen: []byte{},
	}))
	_, ready, err := journal.Open(t.Context(), terminalID, nil)
	require.NoError(t, err)
	for range 2 {
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event:      &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}},
		})
	}
	writer := &pausedSemanticWatchWriter{testResponseWriter: newTestWriter(), sentFirst: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(writer.resume) })
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read terminal:read"))
	go func() {
		defer close(done)
		svc.watchSemanticEvents(ctx, caller, &leapmuxv1.WatchSemanticEventsRequest{
			Tab: terminalTabIdent(terminalID), AfterCursor: ready.GetCursor(),
		}, writer)
	}()
	select {
	case <-writer.sentFirst:
	case <-ctx.Done():
		t.Fatal("the watch did not send the first retained record")
	}
	require.Equal(t, IdentityNewOwner, svc.ApplyWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "another-owner"}))
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the watch did not end after owner revocation")
	}
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 3, "ready, one record and a permission refusal")
	assert.Equal(t, int32(codes.PermissionDenied), messages[2].GetError().GetCode())
	assert.True(t, messages[2].GetEnd())
}

func TestWatchSemanticEventsRequiresTerminalReadBeforeUsingTheCursor(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	writer := newTestWriter()
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read"))
	svc.watchSemanticEvents(t.Context(), caller, &leapmuxv1.WatchSemanticEventsRequest{
		Tab: terminalTabIdent("terminal"), AfterCursor: []byte("invalid cursor"),
	}, writer)
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, int32(codes.PermissionDenied), messages[0].GetError().GetCode())
	assert.Contains(t, messages[0].GetError().GetMessage(), "terminal:read")
}
