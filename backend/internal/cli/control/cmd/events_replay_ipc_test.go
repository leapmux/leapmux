package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type replayIPCWorker struct {
	requests chan *leapmuxv1.WatchSemanticEventsRequest
	attempt  atomic.Int32
}

func (*replayIPCWorker) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, fmt.Errorf("unexpected unary Worker request")
}

func (w *replayIPCWorker) StreamInner(ctx context.Context, target string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	send func(*leapmuxv1.InnerStreamMessage), _ func(channel.StreamController),
) error {
	if target != "worker-B" || req.GetMethod() != semanticWatchProcedure {
		return fmt.Errorf("unexpected stream %s on %s", req.GetMethod(), target)
	}
	request := new(leapmuxv1.WatchSemanticEventsRequest)
	if err := proto.Unmarshal(req.GetPayload(), request); err != nil {
		return err
	}
	select {
	case w.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	attempt := w.attempt.Add(1)
	if attempt > 2 {
		return connect.NewError(connect.CodeFailedPrecondition, "unexpected retry")
	}
	frame := func(value *leapmuxv1.WatchSemanticEventsResponse) error {
		raw, err := proto.Marshal(value)
		if err != nil {
			return err
		}
		send(&leapmuxv1.InnerStreamMessage{Payload: raw})
		return nil
	}
	if err := frame(&leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{
		Ready: &leapmuxv1.SemanticWatchReady{
			Tab: request.GetTab(), WorkerId: target, JournalId: "journal", TabStreamId: "tab-stream",
			HighWaterSequence: int64(attempt - 1), Cursor: []byte("initial position"),
		},
	}}); err != nil {
		return err
	}
	if attempt == 1 {
		return connect.NewError(connect.CodeCanceled, "sibling Worker channel closed")
	}
	return frame(checkpointBell(1))
}

func TestCheckpointedEventsOverIPCResumesAfterTheSiblingWorkerDisconnects(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	terminalID := fullTestID("terminal")
	worker := &replayIPCWorker{requests: make(chan *leapmuxv1.WatchSemanticEventsRequest, 3)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A", "worker-B"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:    &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: terminalID},
			TileId: "root-1", WorkerId: "worker-B", WorkspaceId: "ws-1",
		},
	}, newEventsWorker(), func(r *controlipc.Router) { r.CrossWorker = worker })
	path := filepath.Join(t.TempDir(), "position.json")
	run := runControlLive(t, "events", "--tab", terminalID, "--checkpoint", path, "--limit", "1", "--json")
	assert.Empty(t, receive(t, ctx, worker.requests).GetAfterCursor())
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindStreamUnavailable, run.next(t, ctx).Kind)
	assert.Equal(t, []byte("initial position"), receive(t, ctx, worker.requests).GetAfterCursor())
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	bell := run.next(t, ctx)
	assert.Equal(t, kindTerminalBell, bell.Kind)
	require.NotNil(t, bell.Replayed)
	assert.True(t, *bell.Replayed)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	checkpoint, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer checkpoint.close()
	saved, err := checkpoint.read()
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, int64(1), saved.Sequence)
	assert.Equal(t, []byte("next signed position"), saved.Cursor)
}
