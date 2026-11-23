package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthAuthorizationCodeStore struct{ conn *sqliteConn }

var _ store.OAuthAuthorizationCodeStore = (*oauthAuthorizationCodeStore)(nil)

func (c *sqliteConn) fromDBOAuthAuthorizationCode(r gendb.OauthAuthorizationCode) (store.OAuthAuthorizationCode, error) {
	codeChallenge, err := c.ks.OpenString(r.CodeChallenge, keystore.OAuthAuthCodeChallengeAAD(r.CodeHash))
	if err != nil {
		return store.OAuthAuthorizationCode{}, err
	}
	redirectURI, err := c.ks.OpenString(r.RedirectUri, keystore.OAuthAuthCodeRedirectURIAAD(r.CodeHash))
	if err != nil {
		return store.OAuthAuthorizationCode{}, err
	}
	grantedScopes, err := c.ks.OpenString(r.GrantedScopes, keystore.OAuthAuthCodeGrantedScopesAAD(r.CodeHash))
	if err != nil {
		return store.OAuthAuthorizationCode{}, err
	}
	installationName, err := c.ks.OpenString(r.InstallationName, keystore.OAuthAuthCodeInstallationNameAAD(r.CodeHash))
	if err != nil {
		return store.OAuthAuthorizationCode{}, err
	}
	return store.OAuthAuthorizationCode{
		CodeHash:         r.CodeHash,
		UserID:           r.UserID,
		AuthGeneration:   r.AuthGeneration,
		ClientID:         r.ClientID,
		CodeChallenge:    codeChallenge,
		RedirectURI:      redirectURI,
		GrantedScopes:    grantedScopes,
		InstallationName: installationName,
		Minted:           store.StoredRedemption(r.RedemptionKind).MintedRef(r.MintedRefID),
		StepUp:           store.StoredRef(r.StepUpKind, r.StepUpRefID),
		Redemption:       store.StoredRedemption(r.RedemptionKind),
		CreatedAt:        r.CreatedAt.Time,
		ExpiresAt:        r.ExpiresAt.Time,
		ConsumedAt:       r.ConsumedAt.Ptr(),
	}, nil
}

func (s *oauthAuthorizationCodeStore) Create(ctx context.Context, p store.CreateOAuthAuthorizationCodeParams) error {
	codeChallenge, err := s.conn.ks.SealString(p.CodeChallenge, keystore.OAuthAuthCodeChallengeAAD(p.CodeHash))
	if err != nil {
		return err
	}
	redirectURI, err := s.conn.ks.SealString(p.RedirectURI, keystore.OAuthAuthCodeRedirectURIAAD(p.CodeHash))
	if err != nil {
		return err
	}
	grantedScopes, err := s.conn.ks.SealString(p.GrantedScopes, keystore.OAuthAuthCodeGrantedScopesAAD(p.CodeHash))
	if err != nil {
		return err
	}
	installationName, err := s.conn.ks.SealString(p.InstallationName, keystore.OAuthAuthCodeInstallationNameAAD(p.CodeHash))
	if err != nil {
		return err
	}
	stepUpKind, stepUpRefID := p.StepUp.NullableKind()
	return s.conn.queryError(s.conn.q.CreateOAuthAuthorizationCode(ctx, gendb.CreateOAuthAuthorizationCodeParams{
		CodeHash:         p.CodeHash,
		UserID:           p.UserID.String(),
		AuthGeneration:   p.AuthGeneration,
		ClientID:         p.ClientID,
		CodeChallenge:    codeChallenge,
		RedirectUri:      redirectURI,
		GrantedScopes:    grantedScopes,
		InstallationName: installationName,
		ExpiresAt:        sqltime.NewSQLiteTime(p.ExpiresAt),
		StepUpKind:       stepUpKind,
		StepUpRefID:      stepUpRefID,
		RedemptionKind:   p.Redemption.NullableKind(),
	}))
}

func (s *oauthAuthorizationCodeStore) GetActive(ctx context.Context, codeHash []byte, now time.Time) (*store.OAuthAuthorizationCode, error) {
	row, err := s.conn.q.GetActiveOAuthAuthorizationCode(ctx, gendb.GetActiveOAuthAuthorizationCodeParams{
		CodeHash: codeHash,
		Now:      sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBOAuthAuthorizationCode(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *oauthAuthorizationCodeStore) Consume(ctx context.Context, codeHash []byte, now time.Time) error {
	rows, err := s.conn.rowsAffected(s.conn.q.ConsumeOAuthAuthorizationCode(ctx, gendb.ConsumeOAuthAuthorizationCodeParams{
		CodeHash: codeHash,
		Now:      sqltime.NewSQLiteTime(now),
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
// the replay path needs a CONSUMED row, because only that row identifies the
// credential the first exchange minted.
func (s *oauthAuthorizationCodeStore) Get(ctx context.Context, codeHash []byte) (*store.OAuthAuthorizationCode, error) {
	row, err := s.conn.q.GetOAuthAuthorizationCode(ctx, codeHash)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBOAuthAuthorizationCode(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *oauthAuthorizationCodeStore) ConsumeActiveForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return s.conn.rowsAffected(s.conn.q.ConsumeActiveAuthorizationCodesForUserClient(ctx, gendb.ConsumeActiveAuthorizationCodesForUserClientParams{
		ClientID: clientID,
		UserID:   user.String(),
		Now:      sqltime.NewSQLiteTime(now),
	}))
}

func (s *oauthAuthorizationCodeStore) MarkMinted(ctx context.Context, codeHash []byte, minted store.CredentialRef) error {
	redemption, ok := store.RedemptionForCredentialRef(minted)
	if !ok {
		return store.ErrInvalidArgument
	}
	rows, err := s.conn.rowsAffected(s.conn.q.MarkOAuthAuthorizationCodeMinted(ctx, gendb.MarkOAuthAuthorizationCodeMintedParams{
		CodeHash:               codeHash,
		MintedRefID:            minted.ID,
		ExpectedRedemptionKind: redemption.NullableKind(),
	}))
	if err != nil {
		return err
	}
	if rows != 1 {
		return store.ErrNotFound
	}
	return nil
}
