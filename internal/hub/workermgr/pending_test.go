package workermgr

import (
	"context"
	"testing"
	"time"

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
		Payload: &leapmuxv1.ConnectRequest_FileBrowseResp{
			FileBrowseResp: &leapmuxv1.FileBrowseResponse{
				Path: "/home",
			},
		},
	}

	if !p.Complete("req-1", resp) {
		t.Fatal("expected Complete to return true")
	}

	select {
	case got := <-ch:
		if got.GetFileBrowseResp().GetPath() != "/home" {
			t.Errorf("path = %q, want %q", got.GetFileBrowseResp().GetPath(), "/home")
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestPendingRequests_CompleteUnknown(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	if p.Complete("unknown", &leapmuxv1.ConnectRequest{}) {
		t.Fatal("expected Complete to return false for unknown request")
	}
}

func TestPendingRequests_SendAndWait_NilConn(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })
	_, err := p.SendAndWait(context.Background(), nil, &leapmuxv1.ConnectResponse{})
	require.Error(t, err)
}

func TestPendingRequests_SendAndWait_ContextCancel(t *testing.T) {
	p := NewPendingRequests(func() time.Duration { return 30 * time.Second })

	// Create a conn with nil stream — Send will fail.
	conn := &Conn{WorkerID: "b1", OrgID: "o1"}

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
		OrgID:    "o1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentMsgs <- msg
			return nil
		},
	}

	type result struct {
		resp *leapmuxv1.ConnectRequest
		err  error
	}

	// Launch two concurrent SendAndWait calls.
	agentCh := make(chan result, 1)
	termCh := make(chan result, 1)

	go func() {
		resp, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_AgentStart{
				AgentStart: &leapmuxv1.AgentStartRequest{AgentId: "agent-1"},
			},
		})
		agentCh <- result{resp, err}
	}()

	go func() {
		resp, err := p.SendAndWait(context.Background(), conn, &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_TerminalStart{
				TerminalStart: &leapmuxv1.TerminalStartRequest{TerminalId: "term-1"},
			},
		})
		termCh <- result{resp, err}
	}()

	// Collect both sent messages and identify them by payload type.
	var agentReqID, termReqID string
	for i := 0; i < 2; i++ {
		select {
		case msg := <-sentMsgs:
			switch msg.GetPayload().(type) {
			case *leapmuxv1.ConnectResponse_AgentStart:
				agentReqID = msg.GetRequestId()
			case *leapmuxv1.ConnectResponse_TerminalStart:
				termReqID = msg.GetRequestId()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for sends")
		}
	}

	if agentReqID == "" || termReqID == "" {
		t.Fatalf("missing request IDs: agent=%q, terminal=%q", agentReqID, termReqID)
	}

	// Complete terminal first, then agent (out of order).
	if !p.Complete(termReqID, &leapmuxv1.ConnectRequest{
		RequestId: termReqID,
		Payload: &leapmuxv1.ConnectRequest_TerminalStarted{
			TerminalStarted: &leapmuxv1.TerminalStarted{
				TerminalId:         "term-1",
				ResolvedWorkingDir: "/home/user",
			},
		},
	}) {
		t.Fatal("Complete(terminal) returned false")
	}

	if !p.Complete(agentReqID, &leapmuxv1.ConnectRequest{
		RequestId: agentReqID,
		Payload: &leapmuxv1.ConnectRequest_AgentStarted{
			AgentStarted: &leapmuxv1.AgentStarted{
				AgentId:            "agent-1",
				ResolvedWorkingDir: "/workspace",
			},
		},
	}) {
		t.Fatal("Complete(agent) returned false")
	}

	// Verify each goroutine received its correct response.
	select {
	case r := <-agentCh:
		if r.err != nil {
			t.Fatalf("agent error: %v", r.err)
		}
		if r.resp.GetAgentStarted().GetResolvedWorkingDir() != "/workspace" {
			t.Errorf("agent resolved_working_dir = %q, want %q", r.resp.GetAgentStarted().GetResolvedWorkingDir(), "/workspace")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for agent result")
	}

	select {
	case r := <-termCh:
		if r.err != nil {
			t.Fatalf("terminal error: %v", r.err)
		}
		if r.resp.GetTerminalStarted().GetResolvedWorkingDir() != "/home/user" {
			t.Errorf("terminal resolved_working_dir = %q, want %q", r.resp.GetTerminalStarted().GetResolvedWorkingDir(), "/home/user")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for terminal result")
	}
}
