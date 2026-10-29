package service

import (
	"context"

	"connectrpc.com/connect/v2"
)

// serverCallInfo returns the server-side CallInfo for ctx.
//
// connect.Server.Call attaches one before it invokes interceptors and
// generated handlers, so a live RPC always has it. CallInfoForServerContext
// reports false when the call did not come through Server.Call, which
// connect v2 anticipates and names: a raw ServerFunc invocation, and a test
// calling a handler method on a plain context.
//
// Those calls have no transport, so there is nowhere for response metadata to
// travel. Rather than let an unguarded `info.RequestHeader()` panic on the nil
// CallInfo, this returns a detached zero one: it reads as empty, and a write
// lands on a header nothing flushes. Callers that must distinguish "absent"
// from "empty" should ask CallInfoForServerContext themselves.
func serverCallInfo(ctx context.Context) *connect.CallInfo {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		return info
	}
	return &connect.CallInfo{}
}
