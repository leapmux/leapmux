package service

// This file holds the RFC 7009 revocation endpoint: verifying the presented
// bearer, binding the revocation to the app that owns it, and answering the
// idempotent 200 the endpoint promises a retrying client.

import (
	"context"
	"errors"
	"net/http"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// handleRevoke implements RFC 7009.
//
// It verifies the FULL bearer secret before revoking. RFC 7009 section 2.1
// requires the presented token to be valid; without this check, anyone who
// learns a token_id (which is non-secret -- it is returned in the token
// response and in the delegation mint) could revoke a victim's credential by
// posting `lmx_a<victim_id>_anything`.
//
// Already-revoked and already-expired rows still match the secret and proceed
// (an idempotent re-revoke is a 200), so a client retrying after a brief
// network failure does not need to handle 401. That is RFC 7009 section 2.2's
// requirement as well.
//
// It needs NO SCOPE, because the caller presents the very credential it is
// ending: an app disconnecting itself is the case this endpoint exists for,
// and demanding a scope for it would be demanding a permission to give up
// permissions.
//
// Client authentication follows RFC 7009 section 2.1's own split, validated
// BEFORE the token so a caller that cannot authenticate learns that and
// nothing else. A CONFIDENTIAL app must authenticate (Basic or body secret);
// a PUBLIC app must identify itself with its client_id; and in both cases only
// the app a credential was issued to may end it. The token secret remains the
// other half of the proof -- client authentication alone must not let one app
// tear down another's installations. A delegation bearer carries no app at
// all, so its secret is its whole proof, which is the "none" method the
// metadata document advertises for this endpoint.
func (h *OAuthServerHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	bearer := r.FormValue("token")
	if bearer == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	app, clientErr, internalErr := h.authenticatePresentedClient(r.Context(), r)
	if respondClientAuthFailure(w, clientErr, internalErr, "client lookup for revocation failed") {
		return
	}
	kind, tokenID, err := h.validator.VerifyBearerSecret(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			// RFC 7009 section 2.2 says an INVALID token is still a 200: the
			// client's goal ("this token must not work") already holds, and a
			// distinct answer would let a caller probe which token ids exist.
			w.WriteHeader(http.StatusOK)
		} else {
			writeInternalError(w, "token verification for revocation failed", err)
		}
		return
	}
	if kind == auth.BearerKindAPI {
		bindErr, internalErr := h.bindRevocationToClient(r.Context(), app, tokenID)
		if internalErr != nil {
			writeInternalError(w, "reading the credential's app for revocation failed", internalErr)
			return
		}
		if bindErr != nil {
			writeOAuthErrorBody(w, statusForOAuthError(*bindErr), *bindErr)
			return
		}
	}
	switch kind {
	case auth.BearerKindAPI:
		if _, err := h.store.APITokens().Revoke(r.Context(), tokenID); err != nil {
			writeInternalError(w, "API token revocation failed", err)
			return
		}
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
	case auth.BearerKindDelegation:
		if _, err := h.store.DelegationTokens().Revoke(r.Context(), tokenID); err != nil {
			writeInternalError(w, "delegation token revocation failed", err)
			return
		}
		h.lifecycle.BearerRevoked(auth.BearerKindDelegation, tokenID)
	}
	w.WriteHeader(http.StatusOK)
}

// authenticatePresentedClient runs the token stage's client authentication over
// whichever identity the request carries, and answers nil when it carries
// none. Whether an identity is REQUIRED cannot be known until the code reads
// the credential's own app, so absence is a decision for bindRevocationToClient
// rather than an error here -- and an unknown bearer must still answer 200
// without turning this endpoint into a probe for which client_ids exist.
//
// A RETIRED app authenticates here rather than being refused at the door: the
// retirement cascade already revoked its credentials, and the idempotent branch
// in bindRevocationToClient answers the retrying client with the 200 RFC 7009
// section 2.2 promises.
func (h *OAuthServerHandler) authenticatePresentedClient(ctx context.Context, r *http.Request) (*store.OAuthClient, *oauthErrorResponse, error) {
	if _, _, hasBasic := r.BasicAuth(); !hasBasic && r.FormValue("client_id") == "" {
		return nil, nil, nil
	}
	return h.authenticateClientAllowRevoked(ctx, r)
}

// bindRevocationToClient enforces the second half of RFC 7009 section 2.1:
// the credential may be ended only by the app it was issued to. A
// confidential app must have AUTHENTICATED as that app; a public one must at
// least have NAMED itself. The second return value carries an internal
// failure, which the caller answers with a 500 rather than a refusal that
// would read as "already revoked".
func (h *OAuthServerHandler) bindRevocationToClient(ctx context.Context, presented *store.OAuthClient, tokenID string) (*oauthErrorResponse, error) {
	row, err := h.store.APITokens().GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The secret verified against this row moments ago; a row that
			// cannot be read back is one that left between the two reads,
			// which is a revoke that already happened.
			return nil, nil
		}
		return nil, err
	}
	owner, err := h.store.OAuthClients().Get(ctx, row.ClientID)
	if err != nil {
		return nil, err
	}
	if owner.RevokedAt != nil {
		// A retired app's credentials were revoked by the retirement cascade
		// in the same transaction; repeating that is the idempotent 200 the
		// retrying client expects, not a refusal.
		return nil, nil
	}
	if presented == nil {
		if owner.IsConfidential() {
			body := oauthErrorBody("invalid_client", "client authentication is required for this app")
			return &body, nil
		}
		body := oauthErrorBody("invalid_request", "client_id is required to revoke this app's credential")
		return &body, nil
	}
	if presented.ClientID != owner.ClientID {
		if owner.IsConfidential() {
			body := oauthErrorBody("invalid_client", "this credential was issued to a different app")
			return &body, nil
		}
		body := oauthErrorBody("invalid_grant", "this credential was issued to a different app")
		return &body, nil
	}
	return nil, nil
}
