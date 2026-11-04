package hubrpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/leapmux/leapmux/generated/contracts"
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
		leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
		leapmuxv1connect.WorkspaceServiceLocateTileProcedure,
		leapmuxv1connect.WorkspaceServiceListTabsProcedure,
		leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure,
		// UserCRDT surface.
		leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		leapmuxv1connect.UserCRDTUpdatePresenceProcedure,
		leapmuxv1connect.UserCRDTGetMaterializedProcedure,
		// WorkerManagementService surface.
		leapmuxv1connect.WorkerManagementServiceListWorkersProcedure,
		leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
	}
	for _, procedure := range expected {
		desc, err := hubrpc.Lookup(procedure)
		if assert.NoError(t, err, "procedure %q must be in Registry", procedure) {
			assert.Equal(t, procedure, desc.Procedure, "Lookup(%q) must dispatch to that procedure", procedure)
		}
	}
	assert.Len(t, hubrpc.Registry, len(expected), "Registry size must match the documented method list — update both ends together")
}

func TestLookup_UnknownMethodReturnsTypedError(t *testing.T) {
	_, err := hubrpc.Lookup("/leapmux.v1.WorkspaceService/NotARealMethod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub method not implemented")
	assert.Contains(t, err.Error(), "/leapmux.v1.WorkspaceService/NotARealMethod")
}

// A bare method name is not an address: the registry keys every method by its
// full procedure path, so the name alone finds nothing, even for a method that
// the registry holds.
func TestLookup_RefusesABareMethodName(t *testing.T) {
	_, err := hubrpc.Lookup("LocateTab")
	require.ErrorContains(t, err, "hub method not implemented: LocateTab")
}

// The WatchUser stream is not a unary method, so it is not a Registry entry:
// the worker's hub bridge streams it over /ws/userevents instead.
func TestWatchUserStream_IsTheUserEventsRouteAndNoRegistryEntry(t *testing.T) {
	assert.Equal(t, contracts.WSRouteUserEvents, hubrpc.WatchUserStream)
	_, err := hubrpc.Lookup(hubrpc.WatchUserStream)
	assert.Error(t, err)
}

func TestDescriptor_NewRequestAndNewResponseReturnFreshProtos(t *testing.T) {
	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	require.NoError(t, err)

	req1 := desc.NewRequest()
	req2 := desc.NewRequest()
	// Same concrete type, distinct instances — callers can mutate
	// without aliasing.
	assert.NotSame(t, req1, req2)
	_, okReq := req1.(*leapmuxv1.LocateTabRequest)
	assert.True(t, okReq, "NewRequest must return *LocateTabRequest")

	resp := desc.NewResponse()
	_, okResp := resp.(*leapmuxv1.LocateTabResponse)
	assert.True(t, okResp, "NewResponse must return *LocateTabResponse")
}

// fakeWorkspaceService implements just enough of the connect handler
// surface to drive Invoke through the real wire protocol. Methods we
// don't exercise return Unimplemented, mirroring what a partial
// implementation would do on a real hub.
type fakeWorkspaceService struct {
	leapmuxv1connect.UnimplementedWorkspaceServiceHandler
	locateTabResp *leapmuxv1.LocateTabResponse
	locateTabErr  error
	gotAuth       string
	gotTabID      string
}

func (f *fakeWorkspaceService) LocateTab(ctx context.Context, req *leapmuxv1.LocateTabRequest) (*leapmuxv1.LocateTabResponse, error) {
	// Connect v2 carries request headers on the CallInfo attached to the
	// handler's context, not on the request message.
	info, _ := connect.CallInfoForServerContext(ctx)
	f.gotAuth = info.RequestHeader().Get("Authorization")
	f.gotTabID = req.GetTabId()
	if f.locateTabErr != nil {
		return nil, f.locateTabErr
	}
	if f.locateTabResp == nil {
		return &leapmuxv1.LocateTabResponse{}, nil
	}
	return f.locateTabResp, nil
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
	tab := &leapmuxv1.WorkspaceTab{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "t-123"}}
	svc := &fakeWorkspaceService{locateTabResp: &leapmuxv1.LocateTabResponse{Tab: tab}}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	require.NoError(t, err)
	in := desc.NewRequest().(*leapmuxv1.LocateTabRequest)
	in.TabId = "t-123"
	in.TabType = leapmuxv1.TabType_TAB_TYPE_AGENT
	out := desc.NewResponse().(*leapmuxv1.LocateTabResponse)

	// Invoke dispatches through a caller-built client: the caller owns
	// the transport and the interceptor chain.
	client := connect.NewClient(connecthttp.NewTransport(http.DefaultClient, baseURL))
	require.NoError(t, desc.Invoke(context.Background(), client, in, out))

	// Verify the request reached the service with the expected fields.
	assert.Equal(t, "t-123", svc.gotTabID, "wire round-trip must carry tab_id through")
	// And the response got copied back into the caller's `out` slot.
	require.NotNil(t, out.Tab)
	assert.Equal(t, "t-123", out.Tab.Tab.TabId)
}

func TestInvoke_AppliesProvidedClientOptions(t *testing.T) {
	svc := &fakeWorkspaceService{locateTabResp: &leapmuxv1.LocateTabResponse{}}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	require.NoError(t, err)
	in := desc.NewRequest().(*leapmuxv1.LocateTabRequest)
	out := desc.NewResponse().(*leapmuxv1.LocateTabResponse)

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
	svc := &fakeWorkspaceService{locateTabErr: connect.NewError(connect.CodeNotFound, "no such tab")}
	baseURL := startFakeHub(t, svc)

	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
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
	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	require.NoError(t, err)
	// Use a non-LocateTabRequest proto to provoke the type assertion.
	wrongIn := &leapmuxv1.ListTabsRequest{}
	out := desc.NewResponse()

	client := connect.NewClient(connecthttp.NewTransport(&http.Client{}, "http://unused.invalid"))
	err = desc.Invoke(context.Background(), client, wrongIn, out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request type mismatch",
		"caller should learn the request was the wrong proto type, not a generic network error")
}

func TestInvoke_ResponseTypeMismatchReturnsClearError(t *testing.T) {
	desc, err := hubrpc.Lookup(leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	require.NoError(t, err)
	in := desc.NewRequest()
	// Wrong-shaped response slot — should fail before any network IO.
	wrongOut := &leapmuxv1.ListTabsResponse{}

	client := connect.NewClient(connecthttp.NewTransport(&http.Client{}, "http://unused.invalid"))
	err = desc.Invoke(context.Background(), client, in, wrongOut)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response type mismatch")
}

// TestDescriptors_MessagesMatchTheProcedure checks each entry's request and
// response types against the proto declaration of its procedure, so an entry
// that pairs a procedure with another method's messages fails here instead of
// on the wire.
func TestDescriptors_MessagesMatchTheProcedure(t *testing.T) {
	for procedure, desc := range hubrpc.Registry {
		assert.Equal(t, procedure, desc.Procedure, "Registry must key %s by its procedure", desc.Procedure)
		serviceName, methodName, ok := strings.Cut(strings.TrimPrefix(desc.Procedure, "/"), "/")
		require.True(t, ok, "procedure %q must be /<service>/<method>", desc.Procedure)
		found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(serviceName))
		require.NoError(t, err, "service %q of %q", serviceName, procedure)
		service, ok := found.(protoreflect.ServiceDescriptor)
		require.True(t, ok, "%q must be a service", serviceName)
		declared := service.Methods().ByName(protoreflect.Name(methodName))
		require.NotNil(t, declared, "%s must declare %s", serviceName, methodName)
		assert.False(t, declared.IsStreamingClient() || declared.IsStreamingServer(), "%s must be unary", desc.Procedure)
		assert.Equal(t, declared.Input().FullName(), desc.NewRequest().ProtoReflect().Descriptor().FullName(), "%s request type", desc.Procedure)
		assert.Equal(t, declared.Output().FullName(), desc.NewResponse().ProtoReflect().Descriptor().FullName(), "%s response type", desc.Procedure)
	}
}

// TestDescriptors_NewRequestProducesEmptyProtos sanity-checks every
// entry: NewRequest must return a proto.Message with zero-value
// fields so the worker-bridge `proto.Unmarshal(payload, in)` overlay
// starts from a clean slate.
func TestDescriptors_NewRequestProducesEmptyProtos(t *testing.T) {
	for procedure, desc := range hubrpc.Registry {
		for kind, newMessage := range map[string]func() proto.Message{"NewRequest": desc.NewRequest, "NewResponse": desc.NewResponse} {
			got := newMessage()
			require.NotNil(t, got, "procedure %q %s returned nil", procedure, kind)
			assert.NotSame(t, got, newMessage(), "procedure %q %s must return a fresh message on each call", procedure, kind)
			// Marshalling an empty proto yields the empty byte slice
			// (encoding/wire-format property); any non-empty result
			// indicates accidental field initialization.
			b, err := proto.Marshal(got)
			require.NoError(t, err, "marshal %q %s", procedure, kind)
			assert.Empty(t, b, "%q %s should produce a zero-value proto (empty serialization)", procedure, kind)
		}
	}
}
