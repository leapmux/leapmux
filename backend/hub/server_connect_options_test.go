package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// countingCRDTService is a stand-in UserCRDT handler that records whether the
// request ever reached application code. That is the load-bearing assertion for
// the size cap: connect-go must refuse an oversized body while READING it, so an
// over-cap SubmitOps must never be unmarshaled, authorized, or handed to the
// user's single-writer manager goroutine.
type countingCRDTService struct {
	leapmuxv1connect.UnimplementedUserCRDTHandler
	submits int
	// lastBatches is the batch count the handler actually saw, so the
	// under-cap case proves the request arrived intact rather than merely
	// arriving.
	lastBatches int
}

func (s *countingCRDTService) SubmitOps(
	_ context.Context,
	req *connect.Request[leapmuxv1.SubmitOpsRequest],
) (*connect.Response[leapmuxv1.SubmitOpsResponse], error) {
	s.submits++
	s.lastBatches = len(req.Msg.GetBatches())
	return connect.NewResponse(&leapmuxv1.SubmitOpsResponse{}), nil
}

// submitOpsOfSize builds a SubmitOpsRequest whose marshaled size is at least
// `atLeast` bytes, by padding the batch id. The padding is NOT compressible-
// friendly filler that could slip past the cap: connect-go clients default to
// sending uncompressed requests (see connect.WithSendGzip's doc), and the
// handler's limit is applied to the raw read, so the wire size is the size built
// here.
func submitOpsOfSize(atLeast int) *leapmuxv1.SubmitOpsRequest {
	return &leapmuxv1.SubmitOpsRequest{
		Epoch:   1,
		Batches: []*leapmuxv1.OpBatch{{BatchId: strings.Repeat("p", atLeast)}},
	}
}

// TestConnectOptions_BoundsInboundRequestBodies pins the request-size cap every
// Connect handler on the hub mux is mounted with.
//
// connect-go defaults to allowing ANY request size, so before this the whole
// body of an authenticated SubmitOps was buffered, unmarshaled, and then applied
// on that user's single-writer CRDT goroutine -- one caller could stall every
// other tab's submits for as long as its own batch took to process.
//
// The production option value (connectOptions) is what is mounted here, not a
// hand-rolled copy, so a future edit that drops WithReadMaxBytes from the shared
// builder reddens this test. Interceptors are omitted deliberately: the cap is
// enforced by the protocol reader, BELOW the interceptor chain, and the real
// chain needs a store, a keystore and a shutdown channel.
func TestConnectOptions_BoundsInboundRequestBodies(t *testing.T) {
	svc := &countingCRDTService{}
	path, handler := leapmuxv1connect.NewUserCRDTHandler(svc, connectOptions())
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := leapmuxv1connect.NewUserCRDTClient(srv.Client(), srv.URL)

	// Comfortably under the cap: accepted, and the handler sees the real batch.
	_, err := client.SubmitOps(context.Background(),
		connect.NewRequest(submitOpsOfSize(maxConnectRequestBytes/2)))
	require.NoError(t, err, "a legitimate bulk submit must still be accepted")
	require.Equal(t, 1, svc.submits)
	require.Equal(t, 1, svc.lastBatches, "the under-cap request must arrive intact")

	// Comfortably over it: refused, and application code is never reached.
	_, err = client.SubmitOps(context.Background(),
		connect.NewRequest(submitOpsOfSize(maxConnectRequestBytes+(1<<20))))
	require.Error(t, err, "an over-cap request must be refused, not buffered whole")
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"connect reports an over-limit body as resource_exhausted")
	assert.Equal(t, 1, svc.submits,
		"the oversized body must be rejected while reading -- never unmarshaled and never dispatched")
}

// TestMaxConnectRequestBytes_TracksTheResumeBudget pins the sizing ARGUMENT, not
// just the number. The cap is derived from the budget a resume may read back out
// of the journal: a batch bigger than that is one whose own later resume is
// guaranteed to exceed the budget and force that client onto a full snapshot, so
// accepting it buys nothing. Someone raising one of the two in isolation should
// have to look at the other.
func TestMaxConnectRequestBytes_TracksTheResumeBudget(t *testing.T) {
	assert.Equal(t, crdt.MaxResumeDeltaBytes, maxConnectRequestBytes,
		"the inbound cap is derived from the resume read budget; see maxConnectRequestBytes")
	// And it must stay far above the largest legitimate message on this surface:
	// a relayed ChannelMessage, which the channel layer chunk-caps well below it.
	assert.Greater(t, maxConnectRequestBytes, 1<<20,
		"a cap this low would refuse legitimate bulk submits")
}
