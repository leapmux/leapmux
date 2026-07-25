package channelwire

import (
	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ConnectCodeFromChannelOpenError maps a worker ChannelOpenErrorCode to the
// Connect status the Hub should return to the client. Known non-transient
// rejects fail closed as InvalidArgument / FailedPrecondition; unspecified
// or unknown codes stay Internal so a missing ErrorCode cannot silently
// masquerade as a client-fixable fault.
func ConnectCodeFromChannelOpenError(code leapmuxv1.ChannelOpenErrorCode) connect.Code {
	switch code {
	case leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE:
		return connect.CodeInvalidArgument
	case leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE:
		return connect.CodeFailedPrecondition
	case leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER:
		// Hub protocol bug from the client's POV — not a retryable transient.
		return connect.CodeInternal
	case leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED:
		return connect.CodeInvalidArgument
	default:
		return connect.CodeInternal
	}
}

// NewChannelOpenError builds a failed ChannelOpenResponse with a structured
// error_code. Worker reject paths must go through this (or set ErrorCode
// explicitly) so the Hub mapper can classify the failure.
func NewChannelOpenError(channelID, message string, code leapmuxv1.ChannelOpenErrorCode) *leapmuxv1.ChannelOpenResponse {
	return &leapmuxv1.ChannelOpenResponse{
		ChannelId: channelID,
		Error:     message,
		ErrorCode: code,
	}
}
