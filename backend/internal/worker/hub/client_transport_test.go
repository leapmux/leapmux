package hub

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
)

// TestConnect_DoesNotDialAnHTTPSHubInCleartext covers the defect that made the
// documented production shape unsafe.
//
// The old transport set AllowHTTP and supplied a DialTLSContext that returned a
// PLAIN net.Conn. x/net calls DialTLSContext for `https://` too, so
// `-hub https://hub.example.com` sent an HTTP/2 cleartext preface at a TLS
// port: no encryption, and no certificate check on the connection that carries
// the worker's credential.
//
// Reaching certificate VERIFICATION is what proves the fix. A cleartext preface
// cannot produce this error: the TLS server would answer a handshake failure or
// close, and the client would report a connection error instead.
func TestConnect_DoesNotDialAnHTTPSHubInCleartext(t *testing.T) {
	srv := hubtransporttest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	endpoint, err := hubtransport.New(srv.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)

	client := New(endpoint)
	t.Cleanup(client.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = client.Connect(ctx, "test-token")

	require.Error(t, err)
	var certErr *tls.CertificateVerificationError
	assert.ErrorAs(t, err, &certErr,
		"the worker must verify the hub certificate; got %v", err)
}

// TestConnect_UsesTheHTTP2OnlyLane pins that the worker's own connection
// refuses to degrade. Connect is a bidirectional gRPC stream, which HTTP/1.1
// cannot carry, so a hub with no h2c has to fail with a message that names the
// cause rather than with a protocol error from inside connect-go.
func TestConnect_UsesTheHTTP2OnlyLane(t *testing.T) {
	srv := hubtransporttest.NewHTTP1Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	endpoint, err := hubtransport.New(srv.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)

	client := New(endpoint)
	t.Cleanup(client.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = client.Connect(ctx, "test-token")

	require.Error(t, err)
	assert.ErrorIs(t, err, hubtransport.ErrH2CUnsupported)
	assert.Contains(t, err.Error(), srv.URL, "the failure must name the hub an operator configured")
}
