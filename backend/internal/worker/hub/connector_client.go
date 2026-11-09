package hub

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
)

// newConnectorClient builds the WorkerConnectorService client for every
// Worker call to the Hub: registration and its recovery, the Connect stream,
// the tab listing, and deregistration.
//
// It takes the HTTP2Only lane, and that is a requirement rather than a
// preference: the Connect procedure is a BIDIRECTIONAL stream, which HTTP/1.1
// cannot carry in any RPC protocol. Against a cleartext Hub with no h2c, this
// client fails with hubtransport.ErrH2CUnsupported, which states the URL and
// the remedy, instead of degrading to a protocol on which the Worker could
// never connect. The unary procedures use the same client, because they need
// no lane of their own.
func newConnectorClient(endpoint *hubtransport.Endpoint) leapmuxv1connect.WorkerConnectorServiceClient {
	return NewConnectorClient(endpoint.HTTP2OnlyClient(), endpoint.BaseURL())
}

// NewConnectorClient builds a WorkerConnectorService client with the protocol
// that every Worker speaks, over httpClient. A test that plays the Worker
// against a real Hub builds its client here, so that it speaks exactly what a
// Worker speaks.
//
// The client speaks the Connect protocol, as the browser and the CLI do. The
// Connect protocol reports the result of a unary call in the HTTP status and
// the body, and ends a stream with a message in the body. It needs no HTTP
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
