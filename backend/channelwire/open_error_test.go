package channelwire

import (
	"testing"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectCodeFromChannelOpenError(t *testing.T) {
	for _, tc := range []struct {
		code leapmuxv1.ChannelOpenErrorCode
		want connect.Code
	}{
		{leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_UNSPECIFIED, connect.CodeInternal},
		{leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE, connect.CodeInvalidArgument},
		{leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE, connect.CodeFailedPrecondition},
		{leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER, connect.CodeInternal},
		{leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED, connect.CodeInvalidArgument},
		{leapmuxv1.ChannelOpenErrorCode(99), connect.CodeInternal},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, ConnectCodeFromChannelOpenError(tc.code))
		})
	}
}

func TestNewChannelOpenError(t *testing.T) {
	resp := NewChannelOpenError(
		"ch-1",
		"channel id already active",
		leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
	)
	require.NotNil(t, resp)
	assert.Equal(t, "ch-1", resp.GetChannelId())
	assert.Equal(t, "channel id already active", resp.GetError())
	assert.Equal(t, leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE, resp.GetErrorCode())
	assert.Zero(t, resp.GetMaxMessageSize())
	assert.Empty(t, resp.GetHandshakePayload())
}
