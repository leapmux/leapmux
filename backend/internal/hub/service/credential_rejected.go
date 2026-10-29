package service

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/leapmux/leapmux/generated/contracts"
)

// wrappedConnectError builds a Connect error whose wire message is err's own
// text and whose cause is err itself.
//
// connect.NewError takes a string and serializes only that: a plain
// `connect.NewError(code, err.Error())` would send the same bytes as v1 but
// silently break every errors.Is and errors.As that v1's `NewError(code, err)`
// answered, because nothing would unwrap to what the handler wrapped. Taking
// the error once also keeps the message and the cause from two separate
// evaluations of the same call.
func wrappedConnectError(code connect.Code, err error) *connect.Error {
	return connect.NewError(code, err.Error()).WithCause(err)
}

// A rejected CREDENTIAL and a dead SESSION are opposites for a client, and
// this file is the one place the difference is stated.
//
// Three surfaces answer a rejected credential: the two elevation factor paths,
// passkey management, and passkey sign-in. It lived beside the elevation
// service while only that one used it, which made a rule about every
// credential read as a rule about one of them -- and the surface that then
// grew the same need did not find it.

// CredentialRejectedHeader marks an Unauthenticated whose subject is a
// credential the REQUEST carried -- a step-up password, a step-up assertion
// -- and NOT the session that made the request.
//
// The two are opposites for a client. A browser treats Unauthenticated as
// "your session ended" and signs the user out, which is right for an expired
// cookie and catastrophic here: mistyping the password in a verification
// prompt would end the very session the prompt exists to protect. The code
// stays Unauthenticated, because a rejected credential is what it means; the
// marker says WHICH credential.
// The name is owned by contracts/headers.json, generated into the hub and
// the browser alike.
const CredentialRejectedHeader = contracts.CredentialRejectedHeader

// credentialRejectedError builds an Unauthenticated that a client must not
// read as a dead session. See CredentialRejectedHeader.
//
// ctx supplies the response header the marker rides: connect v2 carries no
// metadata on an error, and the transport writes this header when it writes
// the error status.
func credentialRejectedError(ctx context.Context, err error) *connect.Error {
	out := connect.NewError(connect.CodeUnauthenticated, err.Error()).WithCause(err)
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseHeader().Set(CredentialRejectedHeader, "1")
	}
	return out
}
