package service_test

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// authTestClient is the client half of an auth test server. It reaches both
// services the server mounts -- the sign-in surface and the system surface --
// so a test reads the system information from the same hub whose sign-ins it
// drives.
type authTestClient struct {
	leapmuxv1connect.AuthServiceClient
	leapmuxv1connect.SystemServiceClient
}

// mountAuthAndSystem registers the AuthService and the SystemService built
// over it, as backend/hub/server.go does.
func mountAuthAndSystem(server *connect.Server, authSvc *service.AuthService) {
	leapmuxv1connect.RegisterAuthServiceHandler(server, authSvc)
	leapmuxv1connect.RegisterSystemServiceHandler(server, service.NewSystemService(authSvc))
}

// newAuthTestClient builds the client for an auth test server at baseURL.
func newAuthTestClient(httpClient *http.Client, baseURL string) authTestClient {
	c := connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0)))
	return authTestClient{
		AuthServiceClient:   leapmuxv1connect.NewAuthServiceClient(c),
		SystemServiceClient: leapmuxv1connect.NewSystemServiceClient(c),
	}
}
