package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestFrame_roundTrip_request(t *testing.T) {
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id: 42,
				Method: &desktoppb.Request_ProxyHttp{
					ProxyHttp: &desktoppb.ProxyHttpRequest{
						Method:  "POST",
						Path:    "/api/v1/data",
						Headers: map[string]string{"Content-Type": "application/json"},
						Body:    []byte(`{"key":"value"}`),
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)

	req := got.GetRequest()
	require.NotNil(t, req)
	assert.Equal(t, uint64(42), req.Id)

	proxy := req.GetProxyHttp()
	require.NotNil(t, proxy)
	assert.Equal(t, "POST", proxy.Method)
	assert.Equal(t, "/api/v1/data", proxy.Path)
	assert.Equal(t, []byte(`{"key":"value"}`), proxy.Body)
}

func TestFrame_roundTrip_response(t *testing.T) {
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Response{
			Response: &desktoppb.Response{
				Id: 7,
				Result: &desktoppb.Response_ProxyHttp{
					ProxyHttp: &desktoppb.ProxyHttpResponse{
						Status:  200,
						Headers: map[string]string{"Content-Type": "text/plain"},
						Body:    []byte("hello world"),
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)

	resp := got.GetResponse()
	require.NotNil(t, resp)
	assert.Equal(t, uint64(7), resp.Id)

	proxy := resp.GetProxyHttp()
	require.NotNil(t, proxy)
	assert.Equal(t, int32(200), proxy.Status)
	assert.Equal(t, []byte("hello world"), proxy.Body)
}

func TestFrame_roundTrip_event(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Event{
			Event: &desktoppb.Event{
				Payload: &desktoppb.Event_ChannelMessage{
					ChannelMessage: &desktoppb.ChannelMessageEvent{
						Data: data,
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)

	ev := got.GetEvent()
	require.NotNil(t, ev)
	assert.Equal(t, data, ev.GetChannelMessage().Data)
}

func TestFrame_roundTrip_channelMessage(t *testing.T) {
	data := []byte("encrypted-noise-handshake-data")
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id: 1,
				Method: &desktoppb.Request_SendChannelMessage{
					SendChannelMessage: &desktoppb.SendChannelMessageRequest{
						Data: data,
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)

	req := got.GetRequest()
	require.NotNil(t, req)
	assert.Equal(t, data, req.GetSendChannelMessage().Data)
}

func TestFrame_roundTrip_emptyBinary(t *testing.T) {
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id:     1,
				Method: &desktoppb.Request_GetConfig{GetConfig: &desktoppb.GetConfigRequest{}},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)
	require.NotNil(t, got.GetRequest().GetGetConfig())
}

func TestFrame_roundTrip_multipleFrames(t *testing.T) {
	frames := []*desktoppb.Frame{
		{Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id: 1, Method: &desktoppb.Request_GetConfig{GetConfig: &desktoppb.GetConfigRequest{}},
		}}},
		{Message: &desktoppb.Frame_Response{Response: &desktoppb.Response{
			Id: 1, Result: &desktoppb.Response_BoolValue{BoolValue: &desktoppb.BoolValue{Value: true}},
		}}},
		{Message: &desktoppb.Frame_Event{Event: &desktoppb.Event{
			Payload: &desktoppb.Event_ChannelClose{ChannelClose: &desktoppb.ChannelCloseEvent{}},
		}}},
	}

	var buf bytes.Buffer
	for _, f := range frames {
		require.NoError(t, WriteFrame(&buf, f))
	}

	for i, expected := range frames {
		got, err := ReadFrame(&buf)
		require.NoError(t, err, "frame %d", i)
		assert.Equal(t, expected.GetRequest() != nil, got.GetRequest() != nil, "frame %d", i)
		assert.Equal(t, expected.GetResponse() != nil, got.GetResponse() != nil, "frame %d", i)
		assert.Equal(t, expected.GetEvent() != nil, got.GetEvent() != nil, "frame %d", i)
	}
}

func TestFrame_oversizedFrame(t *testing.T) {
	// Write a varint indicating a frame larger than maxFrameSize.
	var buf bytes.Buffer
	// Encode a varint for maxFrameSize + 1.
	size := uint64(maxFrameSize + 1)
	var varintBuf [10]byte
	n := 0
	for size >= 0x80 {
		varintBuf[n] = byte(size) | 0x80
		size >>= 7
		n++
	}
	varintBuf[n] = byte(size)
	n++
	buf.Write(varintBuf[:n])

	_, err := ReadFrame(&buf)
	require.Error(t, err)
	var sizeErr *protodelim.SizeTooLargeError
	assert.ErrorAs(t, err, &sizeErr)
}

func TestFrame_truncatedInput(t *testing.T) {
	// Write a valid frame, then truncate it.
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id: 1, Method: &desktoppb.Request_GetConfig{GetConfig: &desktoppb.GetConfigRequest{}},
		}},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, frame))

	// Truncate to half.
	data := buf.Bytes()
	truncated := data[:len(data)/2]

	_, err := ReadFrame(bytes.NewReader(truncated))
	require.Error(t, err)
}

func TestFrame_emptyInput(t *testing.T) {
	_, err := ReadFrame(strings.NewReader(""))
	require.Error(t, err)
	assert.ErrorIs(t, err, io.EOF)
}
