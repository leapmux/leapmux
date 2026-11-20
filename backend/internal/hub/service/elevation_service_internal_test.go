package service

import (
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eagerFlushStream stands in for a handler that flushes the response
// headers before its first Send, and records what the deadline header
// held at that flush.
type eagerFlushStream struct {
	info    *connect.CallInfo
	flushed string
}

func (f *eagerFlushStream) Receive(any) error { return nil }

func (f *eagerFlushStream) Send(any) error { return nil }

func (f *eagerFlushStream) SendHeaders() error {
	f.flushed = f.info.ResponseHeader().Get(ElevationExpiresAtHeader)
	return nil
}

// TestElevationMetadataStream_StampsTheDeadlineBeforeAnEagerFlush covers
// the wrapper's second output path. connect flushes the response headers
// on SendHeaders and may ignore later mutations to them, so the slide
// deadline must be on the headers before that flush: an eager flush with
// the stamp missing leaves the client's mirror stale for a whole
// elevation window.
func TestElevationMetadataStream_StampsTheDeadlineBeforeAnEagerFlush(t *testing.T) {
	info := &connect.CallInfo{}
	slide := &elevationSlide{}
	until := time.Now().Add(2 * time.Hour).UTC()
	slide.record(until)
	fake := &eagerFlushStream{info: info}
	wrapped := &elevationMetadataStream{ServerStream: fake, info: info, slide: slide}

	require.NoError(t, wrapped.SendHeaders())

	assert.Equal(t, formatElevationDeadline(until), fake.flushed,
		"the deadline must be stamped before the headers flush")
}
