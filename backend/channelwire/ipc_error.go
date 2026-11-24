package channelwire

import (
	"bytes"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

var hubRelayOrigin = func() *connect.ErrorDetail {
	detail, err := connectproto.NewErrorDetail(wrapperspb.String("leapmux.controlipc.hub_relay"))
	if err != nil {
		panic(fmt.Sprintf("encode the Hub relay error origin: %v", err))
	}
	return detail
}()

// WithHubRelayOrigin marks an error from the Hub relay path of ControlIPC.
func WithHubRelayOrigin(err *connect.Error) *connect.Error {
	return err.WithDetail(hubRelayOrigin)
}

// IsHubRelayError distinguishes a relayed failure from a refusal by the
// local ControlIPC service.
func IsHubRelayError(err error) bool {
	var refusal *connect.Error
	if !errors.As(err, &refusal) {
		return false
	}
	for _, detail := range refusal.Details() {
		if detail.Type == hubRelayOrigin.Type && bytes.Equal(detail.Value, hubRelayOrigin.Value) {
			return true
		}
	}
	return false
}
