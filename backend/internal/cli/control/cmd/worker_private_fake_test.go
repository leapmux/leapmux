package cmd

import (
	"context"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// privateCommandWorker is a fake Worker that serves Notify, RevealTab and
// SetTabStatus over the local IPC socket. It records each request with the
// caller that the router dispatched it under, so a test asserts the exact
// request and the tab that the token stamped. mu guards every field.
type privateCommandWorker struct {
	mu    sync.Mutex
	calls []privateCommandCall
	// refusals, keyed by procedure, make the Worker answer that procedure
	// with an error instead of success.
	refusals map[string]privateCommandRefusal
	// answers, keyed by procedure, set the response that the Worker sends.
	// Without one, Notify and SetTabStatus answer what the request stated, as
	// the real Worker does for a text that needs no trim, so a test that gives
	// an answer of its own proves that the CLI prints the Worker's answer.
	answers map[string]proto.Message
}

// privateCommandCall is one request that the fake Worker received.
type privateCommandCall struct {
	method  string
	caller  channel.Caller
	request proto.Message
}

// privateCommandRefusal is an error that the fake Worker answers with.
type privateCommandRefusal struct {
	code    codes.Code
	message string
}

func (w *privateCommandWorker) DispatchWith(_ context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	var in, reply proto.Message
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerPrivateServiceNotifyProcedure:
		in, reply = &leapmuxv1.NotifyRequest{}, &leapmuxv1.NotifyResponse{}
	case leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure:
		in, reply = &leapmuxv1.ListNotificationsRequest{}, &leapmuxv1.ListNotificationsResponse{}
	case leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure:
		in, reply = &leapmuxv1.ClearNotificationRequest{}, &leapmuxv1.ClearNotificationResponse{}
	case leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure:
		in, reply = &leapmuxv1.RevealTabRequest{}, &leapmuxv1.RevealTabResponse{}
	case leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure:
		in, reply = &leapmuxv1.SetTabStatusRequest{}, &leapmuxv1.SetTabStatusResponse{}
	case leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure:
		in, reply = &leapmuxv1.ListTabStatusesRequest{}, &leapmuxv1.ListTabStatusesResponse{}
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	if err := proto.Unmarshal(req.GetPayload(), in); err != nil {
		_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	w.mu.Lock()
	w.calls = append(w.calls, privateCommandCall{method: req.GetMethod(), caller: caller, request: in})
	refusal, refused := w.refusals[req.GetMethod()]
	answer, answered := w.answers[req.GetMethod()]
	w.mu.Unlock()
	if refused {
		_ = rw.SendError(int32(refusal.code), refusal.message)
		return
	}
	if answered {
		reply = answer
	} else {
		switch got := in.(type) {
		case *leapmuxv1.NotifyRequest:
			reply = &leapmuxv1.NotifyResponse{Id: "test-notification", Title: got.GetTitle(), Body: got.GetBody(), Destination: got.GetDestination()}
		case *leapmuxv1.SetTabStatusRequest:
			key := "default"
			if got.Key != nil {
				key = got.GetKey()
			}
			reply = &leapmuxv1.SetTabStatusResponse{Status: got.GetStatus(), Key: key}
		}
	}
	writeProtoResponse(rw, reply)
}

// recorded returns every request that the fake Worker received, in order.
func (w *privateCommandWorker) recorded() []privateCommandCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]privateCommandCall(nil), w.calls...)
}
