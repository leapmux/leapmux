package cmd

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestCRDTSubmitBatchPinsTheFirstSubmissionEpoch(t *testing.T) {
	client, requests := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{BatchId: "batch"}}})
	bootstrap := &CRDTBootstrap{Epoch: 7}
	batch := &leapmuxv1.OpBatch{BatchId: "batch", Ops: []*leapmuxv1.CrdtOp{{OpId: "operation"}}}
	for _, epoch := range []int64{7, 8} {
		bootstrap.Epoch = epoch
		_, err := crdtSubmitBatch(context.Background(), client, bootstrap, "workspace", batch)
		require.NoError(t, err)
		sent := <-requests
		require.Len(t, sent.GetBatches(), 1)
		assert.EqualValues(t, 7, sent.GetBatches()[0].GetEpoch())
		assert.Equal(t, "batch", sent.GetBatches()[0].GetBatchId())
		require.Len(t, sent.GetBatches()[0].GetOps(), 1)
		assert.Equal(t, "operation", sent.GetBatches()[0].GetOps()[0].GetOpId())
	}
	assert.EqualValues(t, 7, batch.GetEpoch())
}

func TestCRDTSubmitBatchKeepsItsEpochAfterAnRPCFailure(t *testing.T) {
	var attempts atomic.Int32
	client, requests := newCRDTSubmitTestClientWithResponder(t, func() (*leapmuxv1.SubmitOpsResponse, error) {
		if attempts.Add(1) == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, "hub temporarily unavailable")
		}
		return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{
			BatchId: "retry",
			Outcome: &leapmuxv1.BatchResult_Committed{Committed: &leapmuxv1.BatchCommitted{Epoch: 8}},
		}}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bootstrap := &CRDTBootstrap{Epoch: 7}
	batch := &leapmuxv1.OpBatch{BatchId: "retry", Ops: []*leapmuxv1.CrdtOp{{OpId: "operation"}}}
	_, err := crdtSubmitBatch(ctx, client, bootstrap, "workspace", batch)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.EqualValues(t, 1, attempts.Load())
	first := <-requests
	require.Len(t, first.GetBatches(), 1)
	assert.EqualValues(t, 7, first.GetBatches()[0].GetEpoch())
	bootstrap.Epoch = 8
	_, err = crdtSubmitBatch(ctx, client, bootstrap, "workspace", batch)
	require.NoError(t, err)
	require.EqualValues(t, 2, attempts.Load())
	second := <-requests
	assert.True(t, proto.Equal(first, second), "an uncertain retry must preserve the submitted batch identity and body")
	assert.EqualValues(t, 7, batch.GetEpoch())
}

func TestCRDTSubmitBatchPreservesAnAssignedEpoch(t *testing.T) {
	client, requests := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{BatchId: "retry"}}})
	_, err := crdtSubmitBatch(context.Background(), client, &CRDTBootstrap{Epoch: 9}, "workspace", &leapmuxv1.OpBatch{BatchId: "retry", Epoch: 3})
	require.NoError(t, err)
	assert.EqualValues(t, 3, (<-requests).GetBatches()[0].GetEpoch())
}

func TestCRDTSubmitBatchRefusesMissingInputsBeforeTransport(t *testing.T) {
	for _, input := range []struct {
		name      string
		bootstrap *CRDTBootstrap
		batch     *leapmuxv1.OpBatch
		message   string
	}{
		{name: "bootstrap", batch: &leapmuxv1.OpBatch{}, message: "bootstrap is nil"},
		{name: "batch", bootstrap: &CRDTBootstrap{Epoch: 1}, message: "batch is nil"},
	} {
		t.Run(input.name, func(t *testing.T) {
			_, err := crdtSubmitBatch(context.Background(), nil, input.bootstrap, "workspace", input.batch)
			require.ErrorContains(t, err, input.message)
		})
	}
}

func TestCRDTSubmitBatchRefusesAnInvalidResultCount(t *testing.T) {
	for _, count := range []int{0, 2} {
		results := make([]*leapmuxv1.BatchResult, count)
		for i := range results {
			results[i] = &leapmuxv1.BatchResult{BatchId: "batch"}
		}
		client, _ := newCRDTSubmitTestClient(t, &leapmuxv1.SubmitOpsResponse{Results: results})
		_, err := crdtSubmitBatch(context.Background(), client, &CRDTBootstrap{Epoch: 1}, "workspace", &leapmuxv1.OpBatch{BatchId: "batch"})
		assert.ErrorContains(t, err, "expected 1 result")
	}
}

type crdtSubmitTestHub struct {
	leapmuxv1connect.UnimplementedUserCRDTHandler
	requests chan *leapmuxv1.SubmitOpsRequest
	respond  func() (*leapmuxv1.SubmitOpsResponse, error)
}

func (s *crdtSubmitTestHub) SubmitOps(ctx context.Context, request *leapmuxv1.SubmitOpsRequest) (*leapmuxv1.SubmitOpsResponse, error) {
	select {
	case s.requests <- proto.Clone(request).(*leapmuxv1.SubmitOpsRequest):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.respond()
}

func newCRDTSubmitTestClient(t *testing.T, response *leapmuxv1.SubmitOpsResponse) (*control.Client, <-chan *leapmuxv1.SubmitOpsRequest) {
	t.Helper()
	return newCRDTSubmitTestClientWithResponder(t, func() (*leapmuxv1.SubmitOpsResponse, error) { return response, nil })
}

func newCRDTSubmitTestClientWithResponder(t *testing.T, respond func() (*leapmuxv1.SubmitOpsResponse, error)) (*control.Client, <-chan *leapmuxv1.SubmitOpsRequest) {
	t.Helper()
	isolateCLIEnv(t)
	hub := &crdtSubmitTestHub{requests: make(chan *leapmuxv1.SubmitOpsRequest, 2), respond: respond}
	server := connect.NewServer()
	leapmuxv1connect.RegisterUserCRDTHandler(server, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	transport := hubtransporttest.NewServer(t, mux)
	client, err := control.NewClientOrAnonymous(transport.URL)
	require.NoError(t, err)
	return client, hub.requests
}
