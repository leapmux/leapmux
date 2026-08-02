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

func TestPendingRequests_SendAndWait_FencedConn(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	conn, _ := newTestConn(t, "b1", nil, nil)
	conn.Fence()

	_, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{})
	require.ErrorIs(t, err, ErrConnectionClosed)
}

func TestSendAndWait_PrefersBufferedResponseOverDone(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	conn, rec := newAutoDrainedConn(t, "b1", nil)

	errCh := make(chan error, 1)
	respCh := make(chan *leapmuxv1.ConnectRequest, 1)
	go func() {
		resp, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
				ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "ch-1"},
			},
		})
		respCh <- resp
		errCh <- err
	}()

	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.pending) > 0
	}, time.Second, 5*time.Millisecond)

	var reqID string
	require.Eventually(t, func() bool {
		msgs := rec.Messages()
		if len(msgs) == 0 {
			return false
		}
		reqID = msgs[0].GetRequestId()
		return reqID != ""
	}, time.Second, 5*time.Millisecond)

	require.True(t, p.Complete(reqID, &leapmuxv1.ConnectRequest{
		RequestId: reqID,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{ChannelId: "ch-1"},
		},
	}))
	conn.Fence()

	select {
	case err := <-errCh:
		require.NoError(t, err, "buffered response must win over Done")
		got := <-respCh
		require.NotNil(t, got)
		assert.Equal(t, "ch-1", got.GetChannelOpenResp().GetChannelId())
	case <-time.After(time.Second):
		t.Fatal("SendAndWait did not return")
	}
}

func TestSendAndWait_FailsFastWhenTheConnIsFenced(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	conn, rec := newAutoDrainedConn(t, "b1", nil)
	_ = rec

	// Enqueue succeeds; fence while waiting for the response so Done fires
	// before the default timeout.
	errCh := make(chan error, 1)
	go func() {
		_, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
				ChannelOpen: &leapmuxv1.ChannelOpenRequest{ChannelId: "ch-1"},
			},
		})
		errCh <- err
	}()

	// Wait until the request is pending, then fence.
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.pending) > 0
	}, time.Second, 5*time.Millisecond)

	conn.Fence()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, ErrConnectionClosed)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("SendAndWait must fail fast on Done, not burn the default timeout")
	}
}

func TestPendingRequests_OutOfOrder(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })

	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 2)
	conn, pump := newTestConn(t, "b1", func(msg *leapmuxv1.ConnectResponse) error {
		sentMsgs <- msg
		return nil
	}, nil)
	pumpStart(t, pump, conn)

	type result struct {
		resp *leapmuxv1.ConnectRequest
		err  error
	}

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
