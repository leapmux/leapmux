package hub

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
)

// newConnectorClient builds the unary client for Worker control-plane calls.
// The Worker stream uses its own WebSocket.
func newConnectorClient(endpoint *hubtransport.Endpoint) leapmuxv1connect.WorkerConnectorServiceClient {
	return NewConnectorClient(endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout), endpoint.BaseURL())
}

// NewConnectorClient builds a WorkerConnectorService client with the protocol
// that every Worker speaks, over httpClient. A test that plays the Worker
// against a real Hub builds its client here, so that it speaks exactly what a
// Worker speaks.
//
// The client speaks the Connect protocol, as the browser and the CLI do. The
// Connect protocol reports the result of a unary call in the HTTP status and
// the body, and needs no HTTP
// trailer, so a reverse proxy that forwards over HTTP/1.1, or that drops
// trailers, cannot lose the answer to a call that the Hub completed. The gRPC
// protocol would carry the same procedures, but its status travels in a
// trailer, and nginx's proxy_pass drops trailers by default.
func NewConnectorClient(httpClient *http.Client, baseURL string) leapmuxv1connect.WorkerConnectorServiceClient {
	return leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		httpClient,
		baseURL,
		connecthttp.WithReadMaxBytes(0),
	)))
}
