package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

func TestClientServiceAccessors_KeepAuthenticationAndLargeResponses(t *testing.T) {
	// A valid unknown field works for every response type. Its wire size exceeds
	// ConnectRPC's default read limit, so this exercises each accessor's policy.
	body := protowire.AppendTag(nil, 50000, protowire.BytesType)
	body = protowire.AppendBytes(body, make([]byte, 5<<20))
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	c := &Client{HTTPClient: server.Client(), StreamHTTPClient: server.Client(), connectURL: server.URL, bearer: "test-token", accessExpiresAt: time.Now().Add(time.Hour)}
	cases := []struct {
		procedure string
		peer      peerKind
		call      func(context.Context) (proto.Message, error)
	}{
		{procedure: leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.WorkspaceService().ListWorkspaces(ctx, &leapmuxv1.ListWorkspacesRequest{})
		}},
		{procedure: leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.WorkerManagementService().ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
		}},
		{procedure: leapmuxv1connect.UserCRDTGetMaterializedProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.UserCRDT().GetMaterialized(ctx, &leapmuxv1.GetMaterializedRequest{})
		}},
		{procedure: leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.ChannelService().GetWorkerHandshakeParams(ctx, &leapmuxv1.GetWorkerHandshakeParamsRequest{})
		}},
		{procedure: leapmuxv1connect.UserServiceGetTimeoutsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.UserService().GetTimeouts(ctx, &leapmuxv1.GetTimeoutsRequest{})
		}},
		{procedure: leapmuxv1connect.AuthServiceGetSystemInfoProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AuthService().GetSystemInfo(ctx, &leapmuxv1.GetSystemInfoRequest{})
		}},
		{procedure: leapmuxv1connect.AdminSettingsServiceListSettingsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AdminSettingsService().ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
		}},
		{procedure: leapmuxv1connect.AdminUserServiceListUsersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{})
		}},
		{procedure: leapmuxv1connect.AppServiceListAppsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AppService().ListApps(ctx, &leapmuxv1.ListAppsRequest{})
		}},
		{procedure: leapmuxv1connect.AdminWorkerServiceListWorkersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AdminWorkerService().ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{})
		}},
		{procedure: leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return c.AdminIdPService().ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{})
		}},
		{procedure: leapmuxv1connect.ControlIPCServiceWhoamiProcedure, peer: peerWorkerIPC, call: func(ctx context.Context) (proto.Message, error) {
			client, err := c.ControlIPCService()
			if err != nil {
				return nil, err
			}
			return client.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		}},
		{procedure: leapmuxv1connect.ControlIPCServiceWhoamiProcedure, peer: peerWorkerIPC, call: func(ctx context.Context) (proto.Message, error) {
			client, err := c.ControlIPCStreamService()
			if err != nil {
				return nil, err
			}
			return client.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.procedure, func(t *testing.T) {
			c.peer = tc.peer
			response, err := tc.call(t.Context())
			var request *http.Request
			select {
			case request = <-requests:
			default:
			}
			require.NoError(t, err, "every accessor must accept legitimate responses above the library default")
			require.NotNil(t, response)
			assert.Len(t, response.ProtoReflect().GetUnknown(), len(body))
			require.NotNil(t, request)
			assert.Equal(t, tc.procedure, request.URL.Path)
			if tc.peer == peerWorkerIPC {
				assert.Equal(t, "test-token", request.Header.Get("X-LeapMux-Token"))
				assert.Empty(t, request.Header.Get("Authorization"))
			} else {
				assert.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
				assert.Empty(t, request.Header.Get("X-LeapMux-Token"))
			}
		})
	}
}
