package service

import "connectrpc.com/connect"

// A rejected CREDENTIAL and a dead SESSION are opposites for a client, and
// this file is the one place the difference is stated.
//
// Three surfaces answer a rejected credential: the two elevation factor arms,
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
const CredentialRejectedHeader = "Leapmux-Credential-Rejected"

// credentialRejectedError builds an Unauthenticated that a client must not
// read as a dead session. See CredentialRejectedHeader.
func credentialRejectedError(err error) *connect.Error {
	out := connect.NewError(connect.CodeUnauthenticated, err)
	out.Meta().Set(CredentialRejectedHeader, "1")
	return out
}
