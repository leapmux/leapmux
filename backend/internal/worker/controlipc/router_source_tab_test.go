package controlipc_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// The caller of a local dispatch carries the tab of the token that made the
// call, so a handler can stamp it as provenance: Notify names the tab that sent
// a notification. The token store supplies it, never the request, so a
// process cannot claim another tab.
func TestRouter_LocalCallerCarriesTheTokensTab(t *testing.T) {
	cases := []struct {
		name string
		info controlipc.TokenInfo
		want string
	}{
		{
			name: "agent tab",
			info: controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A", TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			want: "agent-1",
		},
		{
			name: "terminal tab",
			info: controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A", TabID: "term-1", TerminalID: "term-1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
			want: "term-1",
		},
		{
			// A quake terminal has no tab: the factory advertises none.
			name: "quake terminal",
			info: controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A", TerminalID: "quake-1"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var unary, streamed channel.Caller
			r := &controlipc.Router{WorkerID: "worker-A", UserID: tc.info.UserID}
			r.LocalDispatcher = funcDispatcher(func(_ context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
				if req.GetMethod() == leapmuxv1connect.WorkerPrivateServiceNotifyProcedure {
					unary = caller
				} else {
					streamed = caller
				}
				_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{})
			})

			_, err := r.CallInner(context.Background(), tc.info, "worker."+leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, nil, "")
			require.NoError(t, err)
			require.NoError(t, r.StreamInner(context.Background(), tc.info,
				"worker."+leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, nil, "", "",
				func(*leapmuxv1.InnerStreamMessage) error { return nil }))

			for name, caller := range map[string]channel.Caller{"unary": unary, "stream": streamed} {
				assert.Equalf(t, tc.want, caller.SourceTabID, "the %s call's source tab", name)
				assert.Truef(t, caller.UserID.MatchesUser(tc.info.UserID), "the %s call runs as the token's user", name)
				assert.Truef(t, caller.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE), "the %s call keeps the local caller's unscoped grant", name)
			}
		})
	}
}
