package service

// This file holds the RFC 6749 section 6 refresh-rotation engine: the
// singleflight that collapses racing refreshes, the rotation itself, the
// scope narrowing a refresh may apply, and the CAS-miss recovery that answers
// a retry. The shared token-response shapes live in oauth_server_token.go,
// beside the mint that also uses them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

type parsedRefreshBearer struct {
	bearer     string
	tokenID    string
	secretHash []byte
}

func (h *OAuthServerHandler) parseAPIRefreshBearer(refresh string) (parsedRefreshBearer, error) {
	kind, tokenID, secret, err := auth.ParseBearer(refresh)
	if err != nil {
		return parsedRefreshBearer{}, err
	}
	if kind != auth.BearerKindAPI {
		return parsedRefreshBearer{}, auth.ErrInvalidToken
	}
	return parsedRefreshBearer{
		bearer:     refresh,
		tokenID:    tokenID,
		secretHash: h.validator.HashSecret(secret),
	}, nil
}

func (b parsedRefreshBearer) flightKey() string {
	return fmt.Sprintf("%d:%s:%x", len(b.tokenID), b.tokenID, b.secretHash)
}

// refreshFlightKey extends the bearer's flight key with the requested scope
// and the presented client credentials.
//
// The scope, so two concurrent refreshes asking for DIFFERENT narrowings are
// not collapsed onto one answer -- a follower would otherwise receive a pair
// whose grant it never asked for.
//
// The credentials, because only the leader's closure runs the RFC 6749
// section 6 client authentication: a follower whose credentials the leader's
// flight never saw would still receive the leader's rotated pair, so a caller
// holding only a leaked refresh secret of a confidential app could race the
// legitimate client's refresh and rotate the credential without ever passing
// client authentication -- the exact protection the secret exists to add.
// Keying on the credentials sends each distinct presentation to its own
// flight, where its own leader runs the authentication and refuses it. They
// enter the key as a SHA-256 digest, never as the raw secret.
func refreshFlightKey(parsed parsedRefreshBearer, requestedScope string, r *http.Request) string {
	clientID, clientSecret := presentedClientCredentials(r)
	digest := sha256.Sum256([]byte(clientID + "\x00" + clientSecret))
	return parsed.flightKey() + "|" + requestedScope + "|" + hex.EncodeToString(digest[:])
}

// refreshRetryResponse re-emits the pair a racing rotation already wrote.
//
// Both deadlines come from the ROW, not from the freshly derived pair: the
// winning rotation is what the row records, and this stage only reproduces its
// answer. Reading the pair here would report a window the store never stored.
//
// The SCOPE keeps this caller's own narrowing. The racing winner wrote its own
// narrowing to the column, and re-emitting the winner's reachable grant would
// hand a caller a response that states permissions it explicitly asked to drop
// -- the exact outcome the flight-key comment promises cannot happen, and
// singleflight cannot dedupe two processes, two hubs, or two keys. The
// reported value is therefore the row's reachable grant intersected with what
// THIS request asked for, which is the widest set this caller may believe it
// holds; the stored column stays the winner's, because only the winner rotates.
func (h *OAuthServerHandler) refreshRetryResponse(row *store.APIToken, pair auth.MintedBearerPair, requestedScope string) refreshResponse {
	if row.ExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no access expiration", row.ID))
	}
	if row.RefreshExpiresAt == nil {
		return refreshInternalError(fmt.Errorf("API token %q has no refresh expiration", row.ID))
	}
	// The un-narrowed retry reports the row's reachable grant; the narrowed
	// one reports the decision, which parses the row itself -- computing both
	// would parse the columns twice for one response.
	scope := ""
	if requestedScope == "" {
		scope = reachableScopeOf(row)
	} else {
		decision, err := h.narrowedRefreshScope(row, requestedScope)
		if err != nil {
			return refreshOAuthError(http.StatusBadRequest, "invalid_scope", err.Error())
		}
		scope = decision.reported
	}
	now := h.now()
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(*row.ExpiresAt, now),
		remainingExpiresIn(*row.RefreshExpiresAt, now),
		scope,
	)
}

// handleTokenRefresh runs the RFC 6749 section 6 refresh grant.
//
// It accepts a NARROWING `scope` parameter, which section 6 permits and which
// is the only direction a refresh may move: the value is intersected with the
// stored grant, so a request for something the account never granted yields the
// granted set rather than the requested one, and a request for LESS persists.
func (h *OAuthServerHandler) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.FormValue("refresh_token")
	if refresh == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	// RFC 6749 section 5.2 makes 400 the status for every token-endpoint
	// error EXCEPT invalid_client, and the distinction is one a client library
	// acts on: 401 says "your client credentials are wrong, authenticate
	// again", 400 says "this grant is finished". A 401 here sent a conformant
	// library back to re-authenticate the CLIENT for a refresh token that had
	// been revoked, which can never succeed.
	parsed, err := h.parseAPIRefreshBearer(refresh)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	requestedScope := r.FormValue("scope")
	// Blocking Do (not DoChan + ctx select) is deliberate: a refresh rotates the
	// token single-use, so once the flight starts, every caller -- including one
	// whose client disconnected -- must run to completion and receive the same
	// rotated pair, or it is left with a rotated-away refresh token and no
	// replacement. flightCtx (WithoutCancel) already decouples the work from the
	// leader's request cancellation. This is why it differs from the read-only
	// bearer-validation singleflight, which is safe to abandon on disconnect.
	//
	// The context and its timer are built INSIDE the closure, because only the
	// leader's closure runs. Built outside, every follower allocated a context
	// and armed a timer that nothing ever read.
	result, _, _ := h.refreshFlight.Do(refreshFlightKey(parsed, requestedScope, r), func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), RefreshWorkTimeout)
		defer cancel()
		return h.refresh(flightCtx, r, parsed, requestedScope), nil
	})
	resp, ok := result.(refreshResponse)
	if !ok {
		// singleflight hands the leader's value to every follower and nil only
		// when the flight errored; the closure above never errors today. The
		// assertion is checked rather than assumed so the refactor that lets
		// it cannot turn that day's refreshes into panics.
		writeInternalError(w, "refresh token request failed",
			fmt.Errorf("refresh flight returned %T", result))
		return
	}
	writeRefreshResponse(w, resp)
}

func (h *OAuthServerHandler) refresh(ctx context.Context, r *http.Request, parsed parsedRefreshBearer, requestedScope string) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}

	// RFC 6749 sections 6 and 3.2.1: a client that was issued credentials
	// authenticates on the refresh grant too. The code, device and revocation
	// stages already demand it; without it here, a leaked confidential refresh
	// bearer rotated freely -- the app secret, the half of the proof the
	// registration exists to add, protected nothing on exactly the stage that
	// mints the long-lived pair. A public client satisfies this by identifying
	// itself with client_id, which the control CLI sends on every refresh.
	if _, clientErr, internalErr := h.authenticateClientOpts(ctx, r, row.ClientID, false); internalErr != nil {
		return refreshInternalError(internalErr)
	} else if clientErr != nil {
		return refreshResponse{status: statusForOAuthError(*clientErr), body: *clientErr}
	}

	now := h.now()
	// This stage clips the refresh window to the credential's absolute lifetime,
	// measured from created_at. Without the clip every rotation would push the
	// window a full RefreshTokenTTL forward, so a client that refreshes weekly
	// would keep ONE browser consent alive for ever.
	refreshTTL := auth.RefreshWindowFor(row.CreatedAt, now)
	if refreshTTL <= 0 {
		// Revoke the ROW, not only the cache. Every other caller of
		// BearerRevoked reaches it on a row the store already revoked -- the
		// validator revokes on a confirmed reuse, and a revoked or expired row
		// is refused before it gets here. This stage is the one that decides a
		// credential is dead by itself, so it must also record it: without the
		// write the access token keeps authenticating until its own expiry, up
		// to an hour after the hub told the client the credential was finished,
		// and the row keeps listing as live in Preferences.
		if _, err := h.store.APITokens().Revoke(ctx, row.ID); err != nil {
			slog.ErrorContext(ctx, "could not revoke the credential that reached its maximum lifetime",
				"token_id", row.ID, "err", err)
		}
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant",
			"this credential reached its maximum lifetime; authorize the app again")
	}

	decision, scopeErr := h.narrowedRefreshScope(row, requestedScope)
	if scopeErr != nil {
		return refreshOAuthError(http.StatusBadRequest, "invalid_scope", scopeErr.Error())
	}

	// This stage clips both windows to the SAME ceiling. Bearer validation reads
	// expires_at alone, so an unclipped access token outlives the absolute
	// lifetime this stage just enforced: the last rotation before the ceiling
	// wrote a full hour past it, and the credential kept authenticating for
	// that hour after the hub already answered "this credential reached its
	// maximum lifetime".
	pair := h.deriveRefreshPair(row, parsed, now, refreshTTL)

	if retry {
		return h.refreshRetryResponse(row, pair, requestedScope)
	}

	// First use of the current refresh: rotate both secrets in place on the
	// existing row. The access secret_hash + expires_at must also advance,
	// otherwise the bearer we hand back (`row.ID` + newAccess) won't validate
	// against `row.SecretHash`, which still hashes the rotated-out access
	// secret. The rotation preserves the previous refresh hash and its grace
	// window so a racing retry can deterministically derive and re-emit this
	// same pair on any Hub.
	prevHash := row.RefreshHash
	prevExp := now.Add(auth.RefreshReuseGrace)
	rotated, err := h.store.APITokens().RotateRefresh(ctx, store.RotateAPITokenRefreshParams{
		ID:                       row.ID,
		NewSecretHash:            pair.AccessHash,
		NewExpiresAt:             &pair.AccessExpiresAt,
		NewRefreshHash:           pair.RefreshHash,
		NewRefreshExpiresAt:      &pair.RefreshExpiresAt,
		PreviousRefreshHash:      prevHash,
		PreviousRefreshExpiresAt: &prevExp,
		NewGrantedScopes:         decision.stored,
	})
	if err != nil {
		return refreshInternalError(err)
	}
	if rotated != 1 {
		return h.recoverRefreshCASMiss(ctx, r, parsed, requestedScope)
	}
	// A NARROWING is a withdrawal of authority, so it runs the full teardown
	// rather than only invalidating the cached secret: an open Noise channel
	// carries the scope set announced at its handshake, and the hub cannot
	// renegotiate a session it cannot read, so closing it is the only way to
	// take the authority back. A widening or an unchanged grant is
	// cache-and-extend, as a plain rotation always was.
	//
	// ONE call takes both effects, so a narrowing refresh cannot leave every
	// channel running at the withdrawn authority because the caller made two
	// calls and only the first matched.
	h.lifecycle.BearerRotated(auth.BearerKindAPI, row.ID, pair.AccessExpiresAt, decision.narrowed)

	// Both deadlines come from the pair, because RotateRefresh just wrote it:
	// here the pair IS the row. now is the instant the pair was derived from,
	// so the two reported windows and the stored ones agree exactly.
	return refreshTokenResponse(
		row.ID,
		pair.AccessBearer,
		pair.RefreshBearer,
		remainingExpiresIn(pair.AccessExpiresAt, now),
		remainingExpiresIn(pair.RefreshExpiresAt, now),
		// What the credential REACHES, not what the column keeps. See
		// narrowedRefreshScope.
		decision.reported,
	)
}

// refreshScopeDecision is what narrowedRefreshScope answers. A struct rather
// than positional values, because stored and reported are adjacent
// same-typed strings a call site can transpose in silence -- the same smell
// consentedGrant and postTouchPollOAuthError exist to remove.
type refreshScopeDecision struct {
	// stored is what the column keeps: the account's CONSENT, narrowed only by
	// what this request asked to give up.
	stored string
	// reported is what the response states in `scope`: the consent intersected
	// with the app's registered ceiling.
	reported string
	// narrowed says whether the REACHABLE grant shrank, which decides whether
	// the rotation is a teardown or an extension.
	narrowed bool
}

// narrowedRefreshScope applies RFC 6749 section 6's scope rule.
//
// A refresh may ask for LESS and never for more, so the requested set is
// intersected with the grant. An empty request keeps the grant unchanged,
// which is what a client that does not care sends.
//
// It answers a refreshScopeDecision rather than positional values; the struct
// states why stored and reported are two different things:
//
//   - stored is what the column keeps: the account's CONSENT, narrowed only by
//     what this request asked to give up. The app's registered ceiling is
//     deliberately not written into it -- see the ceiling paragraph below.
//   - reported is what the response states in `scope`: the consent intersected
//     with that ceiling, which is what loadBearer computes at every validation
//     and therefore what the credential can actually do. Reporting the stored
//     value instead would list a permission the app's next call is refused.
//   - narrowed says whether the REACHABLE grant shrank, which is what decides
//     whether the rotation is a teardown or an extension.
//
// The ceiling is read here and never written. An owner who removes a
// permission from the registration takes it away at once, because validation
// re-reads the ceiling; folding it into the column instead would make the loss
// permanent, so putting the permission back would not restore what the account
// already consented to.
func (h *OAuthServerHandler) narrowedRefreshScope(row *store.APIToken, requested string) (refreshScopeDecision, error) {
	current, err := authscope.Parse(row.GrantedScopes)
	if err != nil {
		return refreshScopeDecision{}, err
	}
	ceiling, err := authscope.Parse(row.ClientScopes)
	if err != nil {
		return refreshScopeDecision{}, err
	}
	// What this credential reaches TODAY, which is what an ask is measured
	// against. Measuring against the stored consent instead would let an app
	// whose registration just lost a permission ask for it and be told yes.
	reachable := current.NarrowTo(ceiling)

	next := current
	if requested != "" {
		asked, parseErr := authscope.Parse(requested)
		if parseErr != nil {
			return refreshScopeDecision{}, parseErr
		}
		// A request for something the credential cannot reach is REFUSED, not
		// quietly intersected away.
		//
		// RFC 6749 section 5.2 defines invalid_scope for exactly this -- a
		// request that "exceeds the scope granted by the resource owner" --
		// and refusing is the safer of the two readings. An app handed a
		// credential silently missing a permission it asked for discovers the
		// loss at its first call, far from the refresh, while its own state
		// says it holds the permission.
		//
		// The MESSAGE states which of the two causes applies, because they
		// ask the operator to do different things: a genuine widening is the
		// app's own bug, while a consent that covers the ask and a
		// registration that no longer does is the owner's edit, and the
		// "never widen" sentence would send them chasing the wrong one.
		if !reachable.Contains(asked) {
			if current.Contains(asked) {
				return refreshScopeDecision{}, errors.New(
					"this app is not registered for every permission it asked for")
			}
			return refreshScopeDecision{}, errors.New("a refresh may narrow a grant and never widen it")
		}
		next = asked.Close()
	}
	storable, err := next.Storable()
	if err != nil {
		return refreshScopeDecision{}, err
	}
	reachableNext := next.NarrowTo(ceiling)
	reportable, err := reachableNext.Storable()
	if err != nil {
		return refreshScopeDecision{}, err
	}
	return refreshScopeDecision{stored: storable, reported: reportable, narrowed: reachableNext != reachable}, nil
}

// reachableScopeOf renders reachableGrantOf as the canonical scope string, for
// the refresh RETRY path.
//
// That path re-emits the pair a racing caller already minted without rotating
// anything, so it reads the row rather than a freshly computed grant. It falls
// back to the stored string when the pair cannot be read, because a refresh
// response is the wrong place to discover a drifted vocabulary -- validation
// already refuses the credential for it.
func reachableScopeOf(row *store.APIToken) string {
	reachable, ok := reachableGrantOf(row.GrantedScopes, row.ClientScopes)
	if !ok {
		return row.GrantedScopes
	}
	value, err := reachable.Storable()
	if err != nil {
		return row.GrantedScopes
	}
	return value
}

func (h *OAuthServerHandler) recoverRefreshCASMiss(ctx context.Context, r *http.Request, parsed parsedRefreshBearer, requestedScope string) refreshResponse {
	row, retry, err := h.validator.ValidateAPIRefresh(ctx, parsed.bearer)
	if err != nil {
		return h.refreshValidationError(parsed.tokenID, err)
	}
	if !retry {
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, row.ID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "token revoked")
	}
	now := h.now()
	pair := h.deriveRefreshPair(row, parsed, now, auth.RefreshWindowFor(row.CreatedAt, now))
	return h.refreshRetryResponse(row, pair, requestedScope)
}

// deriveRefreshPair derives the rotated pair for a refresh of `row`, with
// both windows measured from the credential's created_at so the pair can
// never outlive its absolute lifetime. The rotation and the CAS-miss
// recovery MUST derive identically -- a racing retry re-emits the pair the
// winner minted -- so the expression lives here once instead of in two
// spellings that a future edit could change in only one.
func (h *OAuthServerHandler) deriveRefreshPair(row *store.APIToken, parsed parsedRefreshBearer, now time.Time, refreshTTL time.Duration) auth.MintedBearerPair {
	return h.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI,
		row.ID,
		parsed.secretHash,
		now,
		auth.AccessWindowFor(row.CreatedAt, now),
		refreshTTL,
	)
}

func (h *OAuthServerHandler) refreshValidationError(tokenID string, err error) refreshResponse {
	switch {
	case errors.Is(err, auth.ErrRefreshReused):
		// Refuse to hand out the derived pair after a confirmed reuse — the
		// validator already revoked the row.
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "refresh reuse detected; token revoked")
	case errors.Is(err, auth.ErrTokenRevoked), errors.Is(err, auth.ErrTokenExpired):
		h.lifecycle.BearerRevoked(auth.BearerKindAPI, tokenID)
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "token revoked")
	case errors.Is(err, auth.ErrInvalidToken):
		return refreshOAuthError(http.StatusBadRequest, "invalid_grant", "")
	default:
		return refreshInternalError(err)
	}
}
