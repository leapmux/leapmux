package service

import (
	"bytes"
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
)

var errElevationProofChanged = errors.New("your sign-in method changed during verification; verify again")

// sessionElevationProof identifies the factor that already passed verification.
// The grant validates it against current state while holding the user-auth lock.
type sessionElevationProof interface {
	validate(context.Context, store.Store, *store.User) error
}

type passwordElevationProof struct {
	passwordHash   string
	authGeneration int64
}

func (p passwordElevationProof) validate(_ context.Context, _ store.Store, current *store.User) error {
	if p.passwordHash == "" || !current.FirstCredentialExempt || current.PasswordHash != p.passwordHash || current.AuthGeneration != p.authGeneration {
		return errElevationProofChanged
	}
	return nil
}

type providerElevationProof struct{ providerID, subject string }

func (p providerElevationProof) validate(ctx context.Context, tx store.Store, current *store.User) error {
	if p.providerID == "" || p.subject == "" {
		return errElevationProofChanged
	}
	eligible, err := accountElevatesOnlyThroughAProvider(ctx, tx, current)
	if err != nil {
		return err
	}
	if !eligible {
		return errElevationProofChanged
	}
	link, err := tx.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{ProviderID: p.providerID, ProviderSubject: p.subject})
	if errors.Is(err, store.ErrNotFound) {
		return errElevationProofChanged
	}
	if err != nil {
		return err
	}
	if link.UserID != current.ID {
		return errElevationProofChanged
	}
	return nil
}

type passkeyElevationProof struct {
	identity webauthn.VerifiedCredentialIdentity
}

func (p passkeyElevationProof) validate(ctx context.Context, tx store.Store, current *store.User) error {
	if p.identity.RowID == "" || len(p.identity.CredentialID) == 0 {
		return errElevationProofChanged
	}
	credential, err := tx.PasskeyCredentials().GetByID(ctx, p.identity.RowID)
	if errors.Is(err, store.ErrNotFound) {
		return errElevationProofChanged
	}
	if err != nil {
		return err
	}
	if credential.UserID != current.ID || !bytes.Equal(credential.CredentialID, p.identity.CredentialID) {
		return errElevationProofChanged
	}
	return nil
}
