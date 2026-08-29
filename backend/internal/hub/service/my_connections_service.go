package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// The caller's own CONNECTIONS: one row per credential an app holds for this
// account, and the two ways to end one.
//
// DisconnectApp ends the account's whole authorization of an app.
// RevokeMyAPIToken ends ONE installation of it. The pair is the distinction
// the naming table draws -- a CONNECTION is authorized and disconnected, an
// APP CREDENTIAL is minted and revoked -- and both surfaces are needed: an
// app-level ending alone cannot sign one laptop out, and an
// installation-level ending alone leaves a credential live on every other
// machine.
//
// No RPC here requires an elevated session, deliberately. Listing returns
// metadata only, and both endings can only REDUCE access -- asking somebody
// who has just realized an app is malicious to first find their password is
// the wrong failure mode, and the delay is the attacker's gain. The rule gets
// STRONGER with third-party apps, not weaker.
//
// None is refused in SOLO mode any more, and the refusal was theater: the
// solo rung short-circuits before the bearer rung, so anything that can reach
// the port already has full authority and a credential surface granted nothing
// extra. The useful change was the opposite -- a solo user can now hand an app
// a narrow scoped token and have the scope actually bind.

// myAPITokenToProto maps one stored row to the wire shape. It copies
// METADATA only: SecretHash, RefreshHash and the previous-refresh pair are
// deliberately absent, so no path through this RPC can leak a value that
// would authenticate. TestMyAPITokenCarriesNoSecret pins that.
func myAPITokenToProto(row store.APIToken, currentTokenID string) *leapmuxv1.MyAPIToken {
	// What the credential REACHES, not what its column keeps: the consent
	// intersected with the app's registered ceiling, exactly as validation
	// computes it. This list is what a person reads to decide whether to
	// disconnect, so listing a permission the app's next call is refused would
	// be the one wrong answer here -- and an owner who has just narrowed the
	// registration would see no effect at all.
	//
	// An unreadable value on either side renders as NO permissions rather than
	// as the raw string: a token somebody cannot interpret must not appear as
	// though it were one they can, and the credential itself is already
	// refused at validation for the same reason.
	granted, _ := reachableGrantOf(row.GrantedScopes, row.ClientScopes)
	out := &leapmuxv1.MyAPIToken{
		Id:               row.ID,
		ClientId:         row.ClientID,
		ClientName:       row.ClientName,
		InstallationName: row.InstallationName,
		GrantedScopes:    granted.SortedTokens(),
		// The vouch, stated through the same rule the consent screen reads.
		// The join carries it; leaving it out made the panel label EVERY app
		// "unverified", including the ones an administrator vouched for.
		ClientVerified: store.ClientIsVerified(row.ClientRegistrationSource, row.ClientVerifiedAt),
		CreatedAt:      timestamppb.New(row.CreatedAt),
		Current:        currentTokenID != "" && row.ID == currentTokenID,
	}
	// The optional timestamps go through optTimestamp, the nil-guarded
	// accessor every other proto mapper in this package uses.
	out.LastUsedAt = optTimestamp(row.LastUsedAt)
	if row.RefreshExpiresAt != nil {
		out.RefreshExpiresAt = optTimestamp(row.RefreshExpiresAt)
	} else {
		// The fixed-lifetime kind. Its access expiry IS its whole life,
		// because nothing renews it -- so reporting it here is the opposite
		// of reporting a renewing credential's access expiry, which the
		// branch above deliberately withholds. The two branches are exclusive
		// by construction: mintAPIToken writes a refresh deadline only for
		// the rotating kind.
		out.ExpiresAt = optTimestamp(row.ExpiresAt)
	}
	return out
}

// callerAPITokenID returns the api_tokens row id the request authenticated
// with, or "" when it did not authenticate with one (a browser session, a
// delegation bearer, solo mode). Derived from the caller's own credential,
// never from the request body, so `current` cannot mark another row.
//
// The answer comes from CredentialIdentity.APITokenID, which is the accessor
// written for this question. Re-deriving it from Bearer() here meant TWO
// statements of "an api_tokens bearer, and a delegation bearer is not one",
// and only one of them could be corrected.
func callerAPITokenID(userInfo *auth.UserInfo) string {
	return userInfo.Credential.APITokenID()
}

func (s *UserService) ListMyAPITokens(ctx context.Context, req *connect.Request[leapmuxv1.ListMyAPITokensRequest]) (*connect.Response[leapmuxv1.ListMyAPITokensResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.store.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
		UserID: userInfo.ID,
		// Through NormalizePageParams like every other paginated handler: an
		// omitted limit is the proto3 zero, and the queries read zero as
		// "return no rows", so a caller that simply left it out would get an
		// empty page it could not distinguish from an empty table.
		PageParams: NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	currentTokenID := callerAPITokenID(userInfo)
	tokens := make([]*leapmuxv1.MyAPIToken, 0, len(page.Rows))
	for _, row := range page.Rows {
		tokens = append(tokens, myAPITokenToProto(row, currentTokenID))
	}
	return connect.NewResponse(&leapmuxv1.ListMyAPITokensResponse{
		Tokens:     tokens,
		NextCursor: page.NextCursor,
	}), nil
}

// DisconnectApp ends this account's whole authorization of one app.
//
// The verb the naming table gives a CONNECTION, and the one somebody reaches
// for on deciding an app should no longer have access. RevokeMyAPIToken ends
// one INSTALLATION instead; offering only that would leave a credential live
// on every other machine the app runs on, which is the outcome this exists to
// prevent.
//
// The cascade is scoped to the caller's own rows by the UserID it binds, so
// the same statement that retires an app for everybody (AppService.RevokeApp,
// which binds an empty user) cannot be reached from here. The app's
// REGISTRATION is untouched: an account disconnecting an app says nothing
// about whether the app should exist.
//
// ZERO retired rows is a SUCCESS, not a NotFound. The caller's goal -- "this
// app holds nothing of mine" -- already holds, and answering NotFound would
// make a client that raced a second tab report a failure for a state it wanted.
// It also refuses to confirm whether an unknown client_id identifies a real
// app,
// which the app catalogue's own visibility rule already refuses.
func (s *UserService) DisconnectApp(ctx context.Context, req *connect.Request[leapmuxv1.DisconnectAppRequest]) (*connect.Response[leapmuxv1.DisconnectAppResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	clientID := req.Msg.GetClientId()
	if clientID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("client_id is required"))
	}
	// The rows are READ before the cascade, because each one's lifecycle
	// effects run AFTER the write commits: effects accumulate, and a retried
	// transaction would apply some of them twice.
	refs, err := s.store.OAuthClients().ListUserTokenRefs(ctx, clientID, userInfo.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list app credentials: %w", err))
	}
	// The three writes run in ONE transaction, like the equivalent cascade in
	// AppService.RevokeApp: a failure or crash between them would leave the
	// credentials revoked while an outstanding grant survives, and the grant's
	// whole purpose below is that it must NOT outlive the disconnect.
	var revoked int64
	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		var err error
		revoked, err = tx.OAuthClients().RevokeUserTokens(ctx, clientID, userInfo.ID)
		if err != nil {
			return fmt.Errorf("disconnect app: %w", err)
		}
		// The GRANTS this authorization produced die with it: outstanding
		// authorization codes and approved-but-unpolled device grants stay
		// redeemable for their TTL otherwise, and a consent the account allowed
		// seconds before disconnecting would mint a fresh credential for the app
		// the account just cut off.
		now := s.now().UTC()
		if _, err := tx.OAuthAuthorizationCodes().ConsumeActiveForUserClient(ctx, clientID, userInfo.ID, now); err != nil {
			return fmt.Errorf("spend outstanding authorization codes: %w", err)
		}
		if _, err := tx.DeviceAuthorizations().ConsumeApprovedForUserClient(ctx, clientID, userInfo.ID, now); err != nil {
			return fmt.Errorf("spend outstanding device grants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// ONE batched eviction, exactly as RevokeApp runs its cascade's effects.
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	s.lifecycle.BearerRevokedBatch(auth.BearerKindAPI, ids)
	return connect.NewResponse(&leapmuxv1.DisconnectAppResponse{RevokedCredentialCount: revoked}), nil
}

func (s *UserService) RevokeMyAPIToken(ctx context.Context, req *connect.Request[leapmuxv1.RevokeMyAPITokenRequest]) (*connect.Response[leapmuxv1.RevokeMyAPITokenResponse], error) {
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	// The owner equality lives in the UPDATE, so a token id that belongs to
	// somebody else matches no row, and the handler answers it exactly like a
	// missing one -- the listing is per-user, so a caller has no legitimate
	// way to learn another user's token id, and this refuses to confirm one.
	n, err := s.store.APITokens().RevokeOwned(ctx, store.RevokeOwnedAPITokenParams{
		ID:     req.Msg.GetId(),
		UserID: userInfo.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no such credential"))
	}
	s.lifecycle.BearerRevoked(auth.BearerKindAPI, req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.RevokeMyAPITokenResponse{}), nil
}
