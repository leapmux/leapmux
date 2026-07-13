package auth

import "fmt"

// CredentialIdentity is the immutable authentication credential attached to a
// request or long-lived channel. The zero value represents synthetic solo-mode
// authentication; constructors are the only way to create stored credentials.
type CredentialIdentity struct {
	kind        credentialKind
	id          string
	workspaceID string
	// workerID is the worker that MINTED a delegation token. It bounds where the
	// token may be used (see ChannelService.verifyDelegationWorkerScope); empty
	// for every other kind.
	workerID string
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

// DelegationCredential identifies a workspace-scoped delegation_tokens row minted
// by workerID.
//
// The minting worker is part of the credential because it bounds where the token
// may be used: a worker mints a token carrying the identity of whichever user its
// tab was spawned for, which for a shared workspace is NOT the worker's own owner.
// Without the minter recorded here, such a token authenticates as that user
// everywhere -- including against that user's OWN workers, which the minting
// worker has no business reaching. See ChannelService.verifyDelegationWorkerScope.
//
// The minter is required exactly like the other two: delegation_tokens.worker_id is
// NOT NULL and the sole mint path always records it, so an empty one can only mean a
// code path dropped it. Rejecting it here fails at the bug. WorkerScopeID's
// fail-closed contract below stays as defence in depth for a credential built some
// other way -- but a constructor that validates two of its three required fields
// leaves the security-relevant one to be caught as a runtime denial that reads
// exactly like a genuine cross-tenant refusal.
func DelegationCredential(tokenID, workspaceID, workerID string) CredentialIdentity {
	if tokenID == "" || workspaceID == "" || workerID == "" {
		panic("auth: delegation credential requires token, workspace, and minting worker IDs")
	}
	return CredentialIdentity{kind: credentialDelegation, id: tokenID, workspaceID: workspaceID, workerID: workerID}
}

// WorkerScopeID returns the worker that minted a delegation credential, or an
// empty string for other kinds.
//
// For a delegation credential it is never empty: DelegationCredential rejects an
// empty minter, and the store cannot produce one either (delegation_tokens.worker_id
// is NOT NULL and references workers(id)). The one way to get an empty minter on a
// delegation kind is an in-package struct literal that bypasses the constructor --
// which is why verifyDelegationWorkerScope still refuses an empty minter rather than
// treating "unknown" as "unscoped". Both layers fail closed, so forgetting to thread
// the minter through cannot re-open the cross-tenant hole this field exists to close.
func (c CredentialIdentity) WorkerScopeID() string {
	if c.kind == credentialDelegation {
		return c.workerID
	}
	return ""
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
