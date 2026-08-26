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

// The caller's own CLI credentials.
//
// Neither RPC requires an elevated session, deliberately. Listing returns metadata
// only, and revoking can only REDUCE access -- asking somebody who believes
// a token is stolen to first find their password is the wrong failure mode,
// and the delay is the attacker's gain.

// myAPITokenToProto maps one stored row to the wire shape. It copies
// METADATA only: SecretHash, RefreshHash and the previous-refresh pair are
// deliberately absent, so no path through this RPC can leak a value that
// would authenticate. TestMyAPITokenCarriesNoSecret pins that.
func myAPITokenToProto(row store.APIToken, currentTokenID string) *leapmuxv1.MyAPIToken {
	out := &leapmuxv1.MyAPIToken{
		Id:         row.ID,
		ClientType: row.ClientType,
		ClientName: row.ClientName,
		CreatedAt:  timestamppb.New(row.CreatedAt),
		AdminScope: row.AdminScope,
		Current:    currentTokenID != "" && row.ID == currentTokenID,
	}
	if row.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*row.LastUsedAt)
	}
	if row.RefreshExpiresAt != nil {
		out.RefreshExpiresAt = timestamppb.New(*row.RefreshExpiresAt)
	}
	return out
}

// callerAPITokenID returns the api_tokens row id the request authenticated
// with, or "" when it did not authenticate with one (a browser session, a
// delegation bearer, solo mode). Derived from the caller's own credential,
// never from the request body, so `current` cannot be aimed at another row.
func callerAPITokenID(userInfo *auth.UserInfo) string {
	kind, tokenID, ok := userInfo.Credential.Bearer()
	if !ok || kind != auth.BearerKindAPI {
		return ""
	}
	return tokenID
}

func (s *UserService) ListMyAPITokens(ctx context.Context, req *connect.Request[leapmuxv1.ListMyAPITokensRequest]) (*connect.Response[leapmuxv1.ListMyAPITokensResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "CLI credentials"); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.store.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
		UserID: userInfo.ID,
		// Through NormalizePageParams like every other paginated handler: an
		// omitted limit is the proto3 zero, and the queries read zero as
		// "return no rows", so a caller that simply left it out would get an
		// empty page it could not tell from an empty table.
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

func (s *UserService) RevokeMyAPIToken(ctx context.Context, req *connect.Request[leapmuxv1.RevokeMyAPITokenRequest]) (*connect.Response[leapmuxv1.RevokeMyAPITokenResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "CLI credentials"); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	// The owner equality lives in the UPDATE, so a token id belonging to
	// somebody else matches no row and is answered exactly like a missing
	// one -- the listing is per-user, so a caller has no legitimate way to
	// learn another user's token id, and this refuses to confirm one.
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
