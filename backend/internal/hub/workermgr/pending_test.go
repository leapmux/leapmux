package workermgr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestPendingRequests_Complete(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })

	// We can't use a real stream, so test Complete directly.
	ch := make(chan *leapmuxv1.ConnectRequest, 1)
	p.mu.Lock()
	p.pending["req-1"] = ch
	p.mu.Unlock()

	resp := &leapmuxv1.ConnectRequest{
		RequestId: "req-1",
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
				ChannelId: "ch-1",
			},
		},
	}

	require.True(t, p.Complete("req-1", resp))

	select {
	case got := <-ch:
		assert.Equal(t, "ch-1", got.GetChannelOpenResp().GetChannelId())
	default:
		t.Fatal("expected message on channel")
	}
}

func TestPendingRequests_CompleteUnknown(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	require.False(t, p.Complete("unknown", &leapmuxv1.ConnectRequest{}))
}

func TestPendingRequests_SendAndWait_NilConn(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	_, err := p.SendAndWait(context.Background(), nil, &leapmuxv1.ConnectResponse{})
	require.Error(t, err)
}

func TestPendingRequests_SendAndWait_ContextCancel(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })

	// Create a conn with nil stream — Send will fail.
	conn := &Conn{WorkerID: "b1"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{})
	require.Error(t, err)
}

func TestPendingRequests_OutOfOrder(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })

	// Use a channel to safely capture sent messages from concurrent goroutines.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 2)
	conn := &Conn{
		WorkerID: "b1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentMsgs <- msg
			return nil
		},
	}

	type result struct {
		resp *leapmuxv1.ConnectRequest
		err  error
	}

	// Launch two concurrent SendAndWait calls using ChannelOpen messages.
	ch1Result := make(chan result, 1)
	ch2Result := make(chan result, 1)

	go func() {
		resp, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
				ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "ch-1"},
			},
		})
		ch1Result <- result{resp, err}
	}()

	go func() {
		resp, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
				ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "ch-2"},
			},
		})
		ch2Result <- result{resp, err}
	}()

	// Collect both sent messages and capture their request IDs.
	var reqID1, reqID2 string
	for i := 0; i < 2; i++ {
		select {
		case msg := <-sentMsgs:
			open := msg.GetChannelOpen()
			if open != nil {
				switch open.GetChannelId() {
				case "ch-1":
					reqID1 = msg.GetRequestId()
				case "ch-2":
					reqID2 = msg.GetRequestId()
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for sends")
		}
	}

	require.NotEmpty(t, reqID1, "missing request ID for ch-1")
	require.NotEmpty(t, reqID2, "missing request ID for ch-2")

	// Complete ch-2 first, then ch-1 (out of order).
	require.True(t, p.Complete(reqID2, &leapmuxv1.ConnectRequest{
		RequestId: reqID2,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{ChannelId: "ch-2"},
		},
	}))

	require.True(t, p.Complete(reqID1, &leapmuxv1.ConnectRequest{
		RequestId: reqID1,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{ChannelId: "ch-1"},
		},
	}))

	// Verify each goroutine received its correct response.
	select {
	case r := <-ch1Result:
		require.NoError(t, r.err, "ch-1 error")
		assert.Equal(t, "ch-1", r.resp.GetChannelOpenResp().GetChannelId())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ch-1 result")
	}

	select {
	case r := <-ch2Result:
		require.NoError(t, r.err, "ch-2 error")
		assert.Equal(t, "ch-2", r.resp.GetChannelOpenResp().GetChannelId())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ch-2 result")
	}
}
