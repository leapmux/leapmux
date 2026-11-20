package postgres

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthAuthorizationCodeStore struct{ conn *pgConn }

var _ store.OAuthAuthorizationCodeStore = (*oauthAuthorizationCodeStore)(nil)

func fromDBOAuthAuthorizationCode(c gendb.OauthAuthorizationCode) store.OAuthAuthorizationCode {
	return store.OAuthAuthorizationCode{
		Code:             c.Code,
		UserID:           c.UserID,
		AuthGeneration:   c.AuthGeneration,
		ClientID:         c.ClientID,
		CodeChallenge:    c.CodeChallenge,
		RedirectURI:      c.RedirectUri,
		GrantedScopes:    c.GrantedScopes,
		InstallationName: c.InstallationName,
		Minted:           store.StoredRef(c.MintedKind, c.MintedRefID),
		StepUp:           store.StoredRef(c.StepUpKind, c.StepUpRefID),
		Redemption:       store.StoredRedemption(c.RedemptionKind),
		CreatedAt:        c.CreatedAt.Time,
		ExpiresAt:        c.ExpiresAt.Time,
		ConsumedAt:       c.ConsumedAt.Ptr(),
	}
}

func (s *oauthAuthorizationCodeStore) Create(ctx context.Context, p store.CreateOAuthAuthorizationCodeParams) error {
	stepUpKind, stepUpRefID := p.StepUp.NullableKind()
	return mapErr(s.conn.q.CreateOAuthAuthorizationCode(ctx, gendb.CreateOAuthAuthorizationCodeParams{
		Code:             p.Code,
		UserID:           p.UserID.String(),
		AuthGeneration:   p.AuthGeneration,
		ClientID:         p.ClientID,
		CodeChallenge:    p.CodeChallenge,
		RedirectUri:      p.RedirectURI,
		GrantedScopes:    p.GrantedScopes,
		InstallationName: p.InstallationName,
		ExpiresAt:        pgtime.New(p.ExpiresAt),
		StepUpKind:       stepUpKind,
		StepUpRefID:      stepUpRefID,
		RedemptionKind:   p.Redemption.NullableKind(),
	}))
}

func (s *oauthAuthorizationCodeStore) GetActive(ctx context.Context, code string, now time.Time) (*store.OAuthAuthorizationCode, error) {
	row, err := s.conn.q.GetActiveOAuthAuthorizationCode(ctx, gendb.GetActiveOAuthAuthorizationCodeParams{
		Code: code,
		Now:  pgtime.New(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOAuthAuthorizationCode(row)
	return &out, nil
}

func (s *oauthAuthorizationCodeStore) Consume(ctx context.Context, code string, now time.Time) error {
	rows, err := rowsAffected(s.conn.q.ConsumeOAuthAuthorizationCode(ctx, gendb.ConsumeOAuthAuthorizationCodeParams{
		Code: code,
		Now:  pgtime.New(now),
	}))
	if err != nil {
		return err
	}
	if rows != 1 {
		return store.ErrNotFound
	}
	return nil
}

// Get returns the row whatever its state. See OAuthAuthorizationCodeStore.Get:
// the replay path needs a CONSUMED row, because only that row names the
// credential the first exchange minted.
func (s *oauthAuthorizationCodeStore) Get(ctx context.Context, code string) (*store.OAuthAuthorizationCode, error) {
	row, err := s.conn.q.GetOAuthAuthorizationCode(ctx, code)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOAuthAuthorizationCode(row)
	return &out, nil
}

func (s *oauthAuthorizationCodeStore) ConsumeActiveForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ConsumeActiveAuthorizationCodesForUserClient(ctx, gendb.ConsumeActiveAuthorizationCodesForUserClientParams{
		ClientID: clientID,
		UserID:   user.String(),
		Now:      pgtime.New(now),
	}))
}

func (s *oauthAuthorizationCodeStore) MarkMinted(ctx context.Context, code string, minted store.CredentialRef) error {
	mintedKind, mintedRefID := minted.NullableKind()
	if mintedKind == nil {
		return store.ErrInvalidArgument
	}
	redemptionKind := leapmuxv1.RedemptionKind(*mintedKind)
	rows, err := rowsAffected(s.conn.q.MarkOAuthAuthorizationCodeMinted(ctx, gendb.MarkOAuthAuthorizationCodeMintedParams{
		Code:                   code,
		MintedKind:             mintedKind,
		MintedRefID:            mintedRefID,
		ExpectedRedemptionKind: &redemptionKind,
	}))
	if err != nil {
		return err
	}
	if rows != 1 {
		return store.ErrNotFound
	}
	return nil
}
