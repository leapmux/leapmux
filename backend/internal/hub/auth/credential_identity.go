package auth

import "fmt"

// CredentialIdentity is the immutable authentication credential attached to a
// request or long-lived channel. The zero value represents synthetic solo-mode
// authentication; constructors are the only way to create stored credentials.
type CredentialIdentity struct {
	kind        credentialKind
	id          string
	workspaceID string
}

type credentialKind uint8

const (
	credentialSession credentialKind = iota + 1
	credentialAPI
	credentialDelegation
)

// SessionCredential identifies a cookie-backed user session.
func SessionCredential(sessionID string) CredentialIdentity {
	if sessionID == "" {
		panic("auth: session credential requires an ID")
	}
	return CredentialIdentity{kind: credentialSession, id: sessionID}
}

// APICredential identifies an api_tokens bearer row.
func APICredential(tokenID string) CredentialIdentity {
	if tokenID == "" {
		panic("auth: API credential requires an ID")
	}
	return CredentialIdentity{kind: credentialAPI, id: tokenID}
}

// DelegationCredential identifies a workspace-scoped delegation_tokens row.
func DelegationCredential(tokenID, workspaceID string) CredentialIdentity {
	if tokenID == "" || workspaceID == "" {
		panic("auth: delegation credential requires token and workspace IDs")
	}
	return CredentialIdentity{kind: credentialDelegation, id: tokenID, workspaceID: workspaceID}
}

// SessionID returns the session row ID, or an empty string for other kinds.
func (c CredentialIdentity) SessionID() string {
	if c.kind == credentialSession {
		return c.id
	}
	return ""
}

// BearerRef identifies a bearer token row (api_tokens or delegation_tokens) by
// its table kind and primary key. It is the canonical reverse-index key shared
// by the auth revocation ledger and the channel manager's bearer index, so the
// two cannot disagree on how a bearer row is keyed. Construct it via
// NewBearerRef or CredentialIdentity.BearerRef; the fields are unexported so it
// is only ever built through those (it is used purely as a map key).
type BearerRef struct {
	kind    BearerKind
	tokenID string
}

// NewBearerRef builds a bearer reverse-index key from a table kind and row ID.
func NewBearerRef(kind BearerKind, tokenID string) BearerRef {
	return BearerRef{kind: kind, tokenID: tokenID}
}

// IsValid reports whether this reference names a well-formed bearer row: a valid
// table kind and a non-empty token id. The zero BearerRef is invalid, so bearer
// lifecycle methods reject it the same way they rejected an empty token id before
// the key was made a single typed value.
func (r BearerRef) IsValid() bool {
	return r.kind.IsValid() && r.tokenID != ""
}

// Kind returns the bearer table kind this reference points at. Read-only:
// construction still goes only through NewBearerRef / CredentialIdentity, so the
// "both sides key a bearer identically" guarantee is preserved; the accessors
// exist for observability (logging, tests), not to re-derive the pair.
func (r BearerRef) Kind() BearerKind { return r.kind }

// TokenID returns the bearer row id this reference points at.
func (r BearerRef) TokenID() string { return r.tokenID }

// BearerRef returns this identity's bearer reverse-index key, or ok=false when
// the credential is not a bearer.
func (c CredentialIdentity) BearerRef() (BearerRef, bool) {
	kind, tokenID, ok := c.Bearer()
	if !ok {
		return BearerRef{}, false
	}
	return BearerRef{kind: kind, tokenID: tokenID}, true
}

// Bearer returns the bearer table kind and row ID when this is a bearer.
func (c CredentialIdentity) Bearer() (BearerKind, string, bool) {
	switch c.kind {
	case credentialAPI:
		return BearerKindAPI, c.id, true
	case credentialDelegation:
		return BearerKindDelegation, c.id, true
	default:
		return 0, "", false
	}
}

// WorkspaceScopeID returns the delegation workspace scope, if any.
func (c CredentialIdentity) WorkspaceScopeID() string {
	return c.workspaceID
}

// IsDelegation reports whether this identity is a workspace-scoped delegation
// bearer. Equivalent to WorkspaceScopeID() != "" -- a delegation credential
// always carries a workspace scope and no other kind ever does -- but names the
// intent so call sites stop re-encoding "is a delegation token" as an emptiness
// check on the scope string.
func (c CredentialIdentity) IsDelegation() bool {
	return c.kind == credentialDelegation
}

// Matches reports whether both values identify the same credential row and
// delegation scope.
func (c CredentialIdentity) Matches(other CredentialIdentity) bool {
	return c == other
}

// MatchesSession reports whether this identity names sessionID.
func (c CredentialIdentity) MatchesSession(sessionID string) bool {
	return sessionID != "" && c.kind == credentialSession && c.id == sessionID
}

// PrincipalKey returns a stable CRDT actor key for this credential. Both
// bearer kinds share one key format sourced from Bearer(), so a new bearer
// kind needs no new arm here.
func (c CredentialIdentity) PrincipalKey() string {
	if c.kind == credentialSession {
		return "session:" + c.id
	}
	if kind, id, ok := c.Bearer(); ok {
		return fmt.Sprintf("bearer:%02x:%s", byte(kind), id)
	}
	return ""
}
