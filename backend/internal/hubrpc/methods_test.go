package hubrpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hubrpc"
)

// TestRegistry_KnowsEverySupportedMethod pins the wire surface: any
// new hub method requires updating Registry, which both the worker
// controlipc bridge and the CLI hubcall router read. Forgetting this
// table means agents can't invoke the new method from inside a
// worker session.
func TestRegistry_KnowsEverySupportedMethod(t *testing.T) {
	expected := []string{
		// WorkspaceService surface.
		"GetTab", "LocateTab", "LocateTile", "ListTabs",
		"ListWorkspaces", "GetWorkspace",
		"CreateWorkspace", "RenameWorkspace", "DeleteWorkspace",
		// UserCRDT surface.
		"SubmitOps", "UpdatePresence", "GetMaterialized",
		// WorkerManagementService surface.
		"ListWorkers", "GetWorker",
	}
	for _, m := range expected {
		_, err := hubrpc.Lookup(m)
		assert.NoError(t, err, "method %q must be in Registry", m)
	}
	assert.Len(t, hubrpc.Registry, len(expected), "Registry size must match the documented method list — update both ends together")
}

func TestLookup_UnknownMethodReturnsTypedError(t *testing.T) {
	_, err := hubrpc.Lookup("NotARealMethod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub method not implemented")
	assert.Contains(t, err.Error(), "NotARealMethod")
}

func TestDescriptor_NewRequestAndNewResponseReturnFreshProtos(t *testing.T) {
	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)

	req1 := desc.NewRequest()
	req2 := desc.NewRequest()
	// Same concrete type, distinct instances — callers can mutate
	// without aliasing.
	assert.NotSame(t, req1, req2)
	_, okReq := req1.(*leapmuxv1.GetTabRequest)
	assert.True(t, okReq, "NewRequest must return *GetTabRequest")

	resp := desc.NewResponse()
	_, okResp := resp.(*leapmuxv1.GetTabResponse)
	assert.True(t, okResp, "NewResponse must return *GetTabResponse")
}

// fakeWorkspaceService implements just enough of the connect handler
// surface to drive Invoke through the real wire protocol. Methods we
// don't exercise return Unimplemented, mirroring what a partial
// implementation would do on a real hub.
type fakeWorkspaceService struct {
	leapmuxv1connect.UnimplementedWorkspaceServiceHandler
	getTabResp *leapmuxv1.GetTabResponse
	getTabErr  error
	gotAuth    string
	gotTabID   string
}

func (f *fakeWorkspaceService) GetTab(ctx context.Context, req *leapmuxv1.GetTabRequest) (*leapmuxv1.GetTabResponse, error) {
	// Connect v2 carries request headers on the CallInfo attached to the
	// handler's context, not on the request message.
	info, _ := connect.CallInfoForServerContext(ctx)
	f.gotAuth = info.RequestHeader().Get("Authorization")
	f.gotTabID = req.GetTabId()
	if f.getTabErr != nil {
		return nil, f.getTabErr
	}
	if f.getTabResp == nil {
		return &leapmuxv1.GetTabResponse{}, nil
	}
	return f.getTabResp, nil
}

// startFakeHub spins up an httptest server hosting the fake
// WorkspaceService and returns its baseURL. The returned baseURL plugs
// straight into a connect client for Descriptor.Invoke.
func startFakeHub(t *testing.T, svc *fakeWorkspaceService) string {
	t.Helper()
	mux := http.NewServeMux()
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkspaceServiceHandler(server, svc)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestInvoke_HappyPathRoundTripsRequestAndResponse(t *testing.T) {
	tab := &leapmuxv1.WorkspaceTab{TabId: "t-123", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	svc := &fakeWorkspaceService{getTabResp: &leapmuxv1.GetTabResponse{Tab: tab}}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)
	in := desc.NewRequest().(*leapmuxv1.GetTabRequest)
	in.TabId = "t-123"
	in.WorkspaceId = "ws-1"
	in.TabType = leapmuxv1.TabType_TAB_TYPE_AGENT
	out := desc.NewResponse().(*leapmuxv1.GetTabResponse)

	// Invoke dispatches through a caller-built client: the caller owns
	// the transport and the interceptor chain.
	client := connect.NewClient(connecthttp.NewTransport(http.DefaultClient, baseURL))
	require.NoError(t, desc.Invoke(context.Background(), client, in, out))

	// Verify the request reached the service with the expected fields.
	assert.Equal(t, "t-123", svc.gotTabID, "wire round-trip must carry tab_id through")
	// And the response got copied back into the caller's `out` slot.
	require.NotNil(t, out.Tab)
	assert.Equal(t, "t-123", out.Tab.TabId)
}

func TestInvoke_AppliesProvidedClientOptions(t *testing.T) {
	svc := &fakeWorkspaceService{getTabResp: &leapmuxv1.GetTabResponse{}}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)
	in := desc.NewRequest().(*leapmuxv1.GetTabRequest)
	in.WorkspaceId = "ws-1"
	out := desc.NewResponse().(*leapmuxv1.GetTabResponse)

	// Caller-supplied interceptor must reach the wire — both the CLI
	// (session interceptor) and the worker bridge (bearer interceptor)
	// rely on this seam. Connect v2 builds the interceptor chain into
	// the client, so the caller passes it to connect.NewClient.
	authHeaderInterceptor := func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			// Request headers live on the CallInfo carried by the
			// context; the call methods attach it before the chain
			// runs, so make one only if this ClientFunc sees none.
			info, ok := connect.CallInfoForClientContext(ctx)
			if !ok {
				ctx, info = connect.NewClientContext(ctx)
			}
			info.RequestHeader().Set("Authorization", "Bearer my-token")
			return next(ctx, spec)
		}
	}
	client := connect.NewClient(connecthttp.NewTransport(http.DefaultClient, baseURL), authHeaderInterceptor)
	require.NoError(t, desc.Invoke(context.Background(), client, in, out))

	assert.Equal(t, "Bearer my-token", svc.gotAuth, "Invoke must dispatch through the caller's client so its interceptors reach the wire")
}

func TestInvoke_PropagatesHubErrors(t *testing.T) {
	svc := &fakeWorkspaceService{getTabErr: connect.NewError(connect.CodeNotFound, "no such tab")}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)
	in := desc.NewRequest()
	out := desc.NewResponse()

	client := connect.NewClient(connecthttp.NewTransport(http.DefaultClient, baseURL))
	err = desc.Invoke(context.Background(), client, in, out)
	require.Error(t, err)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "hub-side error must surface as connect.Error so callers can inspect Code()")
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestInvoke_RequestTypeMismatchReturnsClearError(t *testing.T) {
	// Construct a baseURL but never reach it — the descriptor must
	// reject the wrong-shaped request BEFORE issuing the HTTP call.
	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)
	// Use a non-GetTabRequest proto to provoke the type assertion.
	wrongIn := &leapmuxv1.ListTabsRequest{}
	out := desc.NewResponse()

	client := connect.NewClient(connecthttp.NewTransport(&http.Client{}, "http://unused.invalid"))
	err = desc.Invoke(context.Background(), client, wrongIn, out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request type mismatch",
		"caller should learn the request was the wrong proto type, not a generic network error")
}

func TestInvoke_ResponseTypeMismatchReturnsClearError(t *testing.T) {
	desc, err := hubrpc.Lookup("GetTab")
	require.NoError(t, err)
	in := desc.NewRequest()
	// Wrong-shaped response slot — should fail before any network IO.
	wrongOut := &leapmuxv1.ListTabsResponse{}

	client := connect.NewClient(connecthttp.NewTransport(&http.Client{}, "http://unused.invalid"))
	err = desc.Invoke(context.Background(), client, in, wrongOut)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response type mismatch")
}

// TestInvoke_PreservesEachMethodsProcedureURL spot-checks that the
// dispatch table maps every method to the correct procedure URL, so
// SubmitOps doesn't end up routed at ListWorkers.
func TestInvoke_PreservesEachMethodsProcedureURL(t *testing.T) {
	cases := map[string]string{
		"GetTab":          leapmuxv1connect.WorkspaceServiceGetTabProcedure,
		"LocateTab":       leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
		"LocateTile":      leapmuxv1connect.WorkspaceServiceLocateTileProcedure,
		"ListTabs":        leapmuxv1connect.WorkspaceServiceListTabsProcedure,
		"SubmitOps":       leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		"UpdatePresence":  leapmuxv1connect.UserCRDTUpdatePresenceProcedure,
		"ListWorkspaces":  leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		"GetWorkspace":    leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
		"CreateWorkspace": leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure,
		"RenameWorkspace": leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure,
		"DeleteWorkspace": leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure,
		"ListWorkers":     leapmuxv1connect.WorkerManagementServiceListWorkersProcedure,
		"GetWorker":       leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
	}
	for method, want := range cases {
		desc, err := hubrpc.Lookup(method)
		require.NoError(t, err, "Lookup(%q)", method)
		assert.Equal(t, want, desc.Procedure, "method %q must dispatch to %s", method, want)
	}
}

// TestDescriptors_NewRequestProducesEmptyProtos sanity-checks every
// entry: NewRequest must return a proto.Message with zero-value
// fields so the worker-bridge `proto.Unmarshal(payload, in)` overlay
// starts from a clean slate.
func TestDescriptors_NewRequestProducesEmptyProtos(t *testing.T) {
	for method, desc := range hubrpc.Registry {
		got := desc.NewRequest()
		require.NotNil(t, got, "method %q NewRequest returned nil", method)
		// Marshalling an empty proto yields the empty byte slice
		// (encoding/wire-format property); any non-empty result
		// indicates accidental field initialization.
		b, err := proto.Marshal(got)
		require.NoError(t, err, "marshal %q", method)
		assert.Empty(t, b, "%q NewRequest should produce a zero-value proto (empty serialization)", method)
	}
}
