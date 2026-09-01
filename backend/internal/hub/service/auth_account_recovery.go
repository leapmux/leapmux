package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// The account-recovery flow: mint a single-use link, then spend it.
//
// Recovery is mechanism-agnostic ON PURPOSE: the link proves the account's
// verified email address, not the method the user lost, so a password-only,
// passkey-only, or provider-only account recovers through the same RPCs.
// Completing spends the token on a replacement factor, one of two:
//
//   - a new password (CompleteAccountRecoveryPassword) -- the account's first one
//     when it had none -- which revokes every existing passkey;
//   - a new passkey (Begin/FinishAccountRecoveryPasskey) -- an unauthenticated
//     WebAuthn registration whose authorization is the token itself, the way
//     passkey signup's authorization is the signup form. It revokes every
//     existing passkey AND clears the password. Linked providers stay.
//
// The response must not branch on the account's mechanisms either -- that
// would enumerate which accounts are passwordless.
//
// The token is browser-bound the same way the OAuth nonce is (see
// browser_secret.go), and its resend cooldown is the shared one (see
// resend_cooldown.go).

// accountRecoveryExpiry is the lifetime of a recovery link. It is longer
// than a verification code's because it arrives by mail and a user clicks it
// later, rather than reads it off a screen and types it. The cooldown that
// limits how often one may be minted is the shared one -- see
// resend_cooldown.go.
const accountRecoveryExpiry = time.Hour

func (s *AuthService) RequestAccountRecovery(ctx context.Context, req *connect.Request[leapmuxv1.RequestAccountRecoveryRequest]) (*connect.Response[leapmuxv1.RequestAccountRecoveryResponse], error) {
	if s.cfg.SoloMode {
		return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
	}
	identifier := req.Msg.GetIdentifier()
	var user *store.User
	var err error
	if strings.Contains(identifier, "@") {
		user, err = s.store.Users().GetByEmail(ctx, strings.TrimSpace(identifier))
	} else {
		username, slugErr := validate.SanitizeSlug("username", identifier)
		if slugErr != nil {
			return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
		}
		user, err = s.store.Users().GetByUsername(ctx, username)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Every miss path returns the same empty body as a hit. This code does
	// NOT equalize the timing: the hit path sends SMTP mail synchronously,
	// and its dial dominates any padding, so padding the miss path would look
	// like a control and protect nothing. The real anti-enumeration controls
	// are the captcha on this procedure, the uniform response body, and the
	// per-account resend cooldown below.
	targetEmail := user.Email
	if targetEmail == "" {
		return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
	}
	// No IsAdmin exemption here, unlike the auth interceptor's verification
	// control: this predicate asks whether the hub trusts THIS address with a
	// credential-bearing recovery link, and the hub never verified it.
	//
	// An unverified administrator self-recovers with ResendVerificationEmail
	// plus VerifyEmail, which Preferences › Account offers beside the
	// "(unverified)" badge. Resend is the path that seeds a pending row from
	// the stored address; RequestEmailChange does NOT, because its
	// administrator branch writes the new address straight to the email
	// column and clears any pending row.
	if !user.EmailVerified && s.emailVerificationRequired(ctx) {
		return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
	}
	// Cooldown: a recent request keeps its link. Minting a fresh token on
	// every request would flood the inbox and invalidate the link the
	// previous email still carries. The mint below is conditional -- the
	// write lands only when no previous token exists or the previous one
	// was issued at least the cooldown ago -- so a double-submit race
	// cannot mint and send twice and the first email's link stays valid.
	// mintBlockedUntil owns the derivation; see resend_cooldown.go.
	// ONE instant for both, the way completeOAuthReauth states the rule:
	// the cutoff and the expiry answer the same "now", so two reads could
	// disagree inside one request.
	now := s.now().UTC()
	// The emailed recovery secret comes from the shared id mint (crypto-rand
	// backed, 48 chars over a 62-alphabet, ~285 bits), mirroring
	// auth.MintAccessSecret and the session ids.
	rawToken := id.Generate()
	expiresAt := now.Add(accountRecoveryExpiry)
	minted, err := s.store.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
		ID:                         user.ID,
		PendingRecoveryToken:       hashBrowserSecret(rawToken),
		PendingRecoveryExpiresAt:   expiresAt,
		PendingRecoveryUnblockedAt: mintUnblockedAt(now),
		Now:                        now,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !minted {
		// A concurrent or recent request holds a live token: keep it (its
		// email is on the way or already delivered) and answer the same
		// empty body as every other path.
		return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
	}
	if err := s.mail.Send(ctx, s.renderer.AccountRecoveryEmail(targetEmail, rawToken, accountRecoveryExpiry)); err != nil {
		// Report the clear separately from the send. A log line that claims
		// "cleared pending token" while the clear itself failed makes the
		// operator look for the wrong cause of a token that is still live
		// for the full recovery TTL. The blocked-until deadline leaves the
		// failure window, so a mint-send-clear loop costs one attempt per
		// window rather than one SMTP transaction per request. The deadline
		// reads the clock HERE, after the relay answered: a deadline derived
		// from the pre-dial read leaves max(0, window - dial) of blockade,
		// and a relay that fails slowly eats the whole window.
		blockedUntil := failedSendUnblockedAt(s.now(), mailFailureCooldown(ctx, s.set))
		if clearErr := s.store.Users().ClearPendingRecovery(ctx, store.ClearPendingRecoveryParams{
			ID:          user.ID,
			UnblockedAt: blockedUntil,
		}); clearErr != nil {
			slog.WarnContext(ctx, "clear pending account recovery after failed send",
				"user_id", user.ID, "err", clearErr)
		}
		slog.WarnContext(ctx, "account recovery email send failed; cleared token",
			"user_id", user.ID, "err", err)
		// Still return empty success so the response cannot enumerate accounts,
		// but do not leave a live token after a failed send.
		return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
	}
	return connect.NewResponse(&leapmuxv1.RequestAccountRecoveryResponse{}), nil
}

// chargeRecoveryAttempt spends one attempt of the shared budget on the row
// that holds this exact token, and returns that row's user. Both completion
// paths run it first -- the budget is the token's, not one path's, so
// alternating between the two cannot exceed the cap.
func (s *AuthService) chargeRecoveryAttempt(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	hashedToken := hashBrowserSecret(token)
	// Charge one attempt against the row that holds this exact token. The
	// find, the charge, and the token re-check are one statement, so a token
	// cleared by a concurrent recovery cannot surface as a 500.
	now := s.now().UTC()
	charged, err := s.store.Users().ConsumeRecoveryAttemptByToken(ctx, hashedToken, now, maxAccountRecoveryAttempts)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired recovery token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Attempt 6+ expires the token in SQL (sets expires_at = now). Refuse
	// before any expensive work so the attempt budget is a hard cap, not soft.
	if charged.PendingRecoveryAttempts > maxAccountRecoveryAttempts {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired recovery token"))
	}
	if charged.PendingRecoveryExpiresAt != nil && now.After(*charged.PendingRecoveryExpiresAt) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("recovery token expired"))
	}
	// The row's PendingRecoveryToken is already the hash the caller must
	// bind at completion -- ConsumeRecoveryAttemptByToken selects the row
	// by it -- so the caller reads it straight off the returned user.
	return charged, nil
}

func (s *AuthService) CompleteAccountRecoveryPassword(ctx context.Context, req *connect.Request[leapmuxv1.CompleteAccountRecoveryPasswordRequest]) (*connect.Response[leapmuxv1.CompleteAccountRecoveryPasswordResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "account recovery"); err != nil {
		return nil, err
	}
	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	user, err := s.chargeRecoveryAttempt(ctx, req.Msg.GetToken())
	if err != nil {
		return nil, err
	}
	hashedToken := user.PendingRecoveryToken

	hashed, err := pwdhash.Hash(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	uid, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	revoked, err := s.spendRecoveryToken(ctx, uid, user.ID, hashedToken, hashed, true, nil)
	if err != nil {
		return nil, err
	}
	if revoked != nil {
		s.lifecycle.UserRevoked(user.ID, revoked.AuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.CompleteAccountRecoveryPasswordResponse{}), nil
}

func (s *AuthService) BeginAccountRecoveryPasskey(ctx context.Context, req *connect.Request[leapmuxv1.BeginAccountRecoveryPasskeyRequest]) (*connect.Response[leapmuxv1.BeginAccountRecoveryPasskeyResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "account recovery"); err != nil {
		return nil, err
	}
	token := req.Msg.GetToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	// Origin and WebAuthn preconditions run BEFORE the attempt charge: a
	// FailedPrecondition cannot enroll, so it must not burn the shared
	// budget that password completion also uses.
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err := wa.CheckOrigin(originFromRequest(req)); err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	// Charge before the ceremony starts, so the attempt cap covers the
	// whole flow: an attacker who cannot produce the token gets the same
	// five guesses against this path as against the password one, and a
	// user who keeps cancelling the browser prompt cannot re-Begin forever
	// on one charge.
	user, err := s.chargeRecoveryAttempt(ctx, token)
	if err != nil {
		return nil, err
	}
	sessionID, optionsJSON, err := wa.BeginRecoveryRegistration(ctx, user.ID, originFromRequest(req), user.PendingRecoveryToken)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginAccountRecoveryPasskeyResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
	}), nil
}

func (s *AuthService) FinishAccountRecoveryPasskey(ctx context.Context, req *connect.Request[leapmuxv1.FinishAccountRecoveryPasskeyRequest]) (*connect.Response[leapmuxv1.FinishAccountRecoveryPasskeyResponse], error) {
	if err := rejectSolo(s.cfg.SoloMode, "account recovery"); err != nil {
		return nil, err
	}
	if req.Msg.GetToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	if req.Msg.GetSessionId() == "" || req.Msg.GetCredentialJson() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session and credential are required"))
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	hashedToken := hashBrowserSecret(req.Msg.GetToken())
	// Refuse a force-expired or reminted token BEFORE consuming the
	// ceremony, so a dead link cannot burn a live session and a reminted
	// token cannot hitchhike on a ceremony Begin charged under the
	// previous hash.
	live, err := s.store.Users().GetByLiveRecoveryToken(ctx, hashedToken, s.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired recovery token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Verify the attestation OUTSIDE the write transaction, the ordering
	// VerifyRegistration states for the SQLite single-writer lock. Finish
	// charges no attempt: the ceremony session it consumes is single-use,
	// and only the captcha'd Begin could mint one. The session also binds
	// the token hash Begin charged.
	userID, cred, err := wa.VerifyRecoveryRegistration(ctx, req.Msg.GetSessionId(), req.Msg.GetCredentialJson(), hashedToken)
	if err != nil {
		return nil, mapPasskeyConnectError(ctx, err)
	}
	if userID != live.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired recovery token"))
	}
	uid, mintErr := mintRowUserID(userID)
	if mintErr != nil {
		return nil, mintErr
	}
	revoked, err := s.spendRecoveryToken(ctx, uid, userID, hashedToken, pwdhash.PlaceholderHash, false, func(tx store.Store) error {
		_, err := wa.StoreCredential(ctx, tx, id.Generate(), userID, cred, "Recovered passkey")
		return err
	})
	if err != nil {
		return nil, err
	}
	if revoked != nil {
		s.lifecycle.UserRevoked(userID, revoked.AuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.FinishAccountRecoveryPasskeyResponse{}), nil
}

// spendRecoveryToken spends the live recovery token inside the user-auth
// transaction and revokes every other authenticator. afterPasskeys runs
// after existing passkeys are deleted and before bearer revocation, so a
// passkey spend can INSERT the newly attested credential in the same
// transaction. A nil afterPasskeys is the password spend.
func (s *AuthService) spendRecoveryToken(
	ctx context.Context,
	uid userid.UserID,
	userID, hashedToken, passwordHash string,
	passwordSet bool,
	afterPasskeys func(tx store.Store) error,
) (*store.RecoveryRevocation, error) {
	var revoked *store.RecoveryRevocation
	err := s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		var completeErr error
		revoked, completeErr = tx.Users().CompleteRecovery(ctx, store.CompleteRecoveryParams{
			ID:                   userID,
			PasswordHash:         passwordHash,
			PasswordSet:          passwordSet,
			PendingRecoveryToken: hashedToken,
		})
		if completeErr != nil {
			if errors.Is(completeErr, store.ErrNotFound) {
				return connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired recovery token"))
			}
			return completeErr
		}
		if err := RevokePasskeyAuthState(ctx, tx, userID); err != nil {
			return err
		}
		if afterPasskeys != nil {
			if err := afterPasskeys(tx); err != nil {
				return err
			}
		}
		// CompleteRecovery's row update already bumped the auth
		// generation and returned the committed epoch, so the bearer
		// revocation takes the already-bumped shape.
		if _, _, _, err := RevokeCredentialsAfterRotation(ctx, tx, uid, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, recoveryTxError(err)
	}
	return revoked, nil
}

func recoveryTxError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	return connect.NewError(connect.CodeInternal, err)
}
