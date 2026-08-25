package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	passwordResetExpiry = time.Hour
	// passwordResetResendCooldown limits how often one account can mint a
	// fresh reset token, mirroring the verification-email resend cooldown.
	passwordResetResendCooldown = 60 * time.Second
)

// verificationOutcome carries the verification flags every login and
// sign-up response reports. Not login-only: the four sign-up flavors and
// the GetCurrentUser bootstrap build one too, and each used to hand-write
// the proto literal instead.
type verificationOutcome struct {
	Required              bool
	EmailSent             bool
	NextResendAvailableAt *time.Time
}

// emailVerificationToProto maps a verification outcome onto the shared
// response message. Every response that carries the status builds it here.
func emailVerificationToProto(out verificationOutcome) *leapmuxv1.EmailVerificationStatus {
	return &leapmuxv1.EmailVerificationStatus{
		VerificationRequired:  out.Required,
		VerificationEmailSent: out.EmailSent,
		NextResendAvailableAt: protoTimestamp(out.NextResendAvailableAt),
	}
}

// verificationStatusFor REPORTS the account's verification state without
// sending anything. loginVerificationOutcome is the sending twin, and the
// difference matters: this one serves GetCurrentUser, which every page load
// calls, so issuing mail from it would send a message per reload.
//
// The cooldown it reports comes from the live pending row, so a hard reload
// of /verify-email resumes the same countdown a login handed out instead of
// starting at zero and letting the button hammer a refusal.
func (s *AuthService) verificationStatusFor(ctx context.Context, user *store.User) verificationOutcome {
	if !s.emailVerificationRequired(ctx) || user.IsAdmin || user.EmailVerified {
		return verificationOutcome{}
	}
	out := verificationOutcome{Required: true}
	if user.PendingEmail != "" && user.PendingEmailExpiresAt != nil {
		next := nextResendAt(issuedAtFromExpiry(*user.PendingEmailExpiresAt, pendingEmailExpiry))
		if time.Now().UTC().Before(next) {
			out.NextResendAvailableAt = &next
		}
	}
	return out
}

func (s *AuthService) loginVerificationOutcome(ctx context.Context, user *store.User) verificationOutcome {
	if !s.emailVerificationRequired(ctx) || user.IsAdmin || user.EmailVerified {
		return verificationOutcome{}
	}
	out := verificationOutcome{Required: true}
	targetEmail := user.PendingEmail
	if targetEmail == "" {
		targetEmail = user.Email
	}
	if targetEmail == "" {
		return out
	}
	if user.PendingEmail == "" {
		sent, next, err := s.ensurePendingVerification(ctx, user.ID, targetEmail)
		if err != nil {
			slogWarnVerification(ctx, "ensure pending verification on login", user.ID, err)
			return out
		}
		out.EmailSent = sent
		out.NextResendAvailableAt = next
		return out
	}
	if user.PendingEmailExpiresAt != nil {
		next := nextResendAt(issuedAtFromExpiry(*user.PendingEmailExpiresAt, pendingEmailExpiry))
		if time.Now().UTC().Before(next) {
			out.NextResendAvailableAt = &next
			return out
		}
	}
	sent, err := issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, user.ID, user.PendingEmail)
	if err != nil {
		slogWarnVerification(ctx, "resend verification on login", user.ID, err)
		return out
	}
	out.EmailSent = sent
	if sent {
		next := nextResendAt(time.Now().UTC())
		out.NextResendAvailableAt = &next
	}
	return out
}

func (s *AuthService) ensurePendingVerification(ctx context.Context, userID, email string) (sent bool, nextResend *time.Time, err error) {
	if err := CheckEmailAvailable(ctx, s.store, email, userID); err != nil {
		return false, nil, err
	}
	sent, err = issuePendingEmailVerification(ctx, s.store, s.mail, s.renderer, userID, email)
	if err != nil {
		return false, nil, err
	}
	if sent {
		next := nextResendAt(time.Now().UTC())
		nextResend = &next
	}
	return sent, nextResend, nil
}

func slogWarnVerification(ctx context.Context, msg, userID string, err error) {
	slog.WarnContext(ctx, msg, "user_id", userID, "err", err)
}

func protoTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func (s *AuthService) BeginPasskeyLogin(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeyLoginRequest]) (*connect.Response[leapmuxv1.BeginPasskeyLoginResponse], error) {
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	user, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Same code/message as "no passkeys" so callers cannot enumerate.
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey login is not available for this account"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID, optionsJSON, rpID, err := wa.BeginLogin(ctx, user.ID, originFromRequest(req))
	if err != nil {
		if classifyWebAuthnError(err) == webAuthnErrorUnavailable {
			// An unserved origin names the remediation. Everything else in
			// this class answers with the same code and message as the
			// missing-user path, so the error is not an enumeration oracle.
			if errors.Is(err, hubwebauthn.ErrOriginNotAllowed) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey login is not available on this origin; open the hub through its configured URL"))
			}
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey login is not available for this account"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyLoginResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

func (s *AuthService) FinishPasskeyLogin(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyLoginRequest]) (*connect.Response[leapmuxv1.FinishPasskeyLoginResponse], error) {
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	user, passkeyCount, err := wa.FinishLogin(ctx, req.Msg.GetSessionId(), req.Msg.GetCredentialJson())
	if err != nil {
		switch classifyWebAuthnError(err) {
		case webAuthnErrorClone:
			// A clone warning is a security event, not a login failure: log
			// it server-side and report it as itself, so it never counts
			// against the login rate-limit budget.
			slog.WarnContext(ctx, "passkey clone warning during login")
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		case webAuthnErrorCredential:
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		case webAuthnErrorUnavailable:
			// Same code Begin answers for the same state. These sentinels
			// describe the hub and the origin, not the account, so the
			// enumeration argument that collapses a credential failure into
			// Unauthenticated does not reach them -- and reporting a hub
			// misconfiguration as "authentication failed" tells the user to
			// try another credential for something no credential can fix.
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		case webAuthnErrorInfrastructure:
			// Store and infrastructure failures (keystore decrypt, session
			// consume, sign-count update) are not credential failures, so
			// they must not read as one in the anonymous client's response
			// body.
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	loginUID, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	sessionID, expiresAt, err := auth.CreateSession(ctx, s.store, loginUID, s.sessionDuration(ctx))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}
	resp := connect.NewResponse(&leapmuxv1.FinishPasskeyLoginResponse{
		User:              userToProto(user, passkeyCount),
		EmailVerification: emailVerificationToProto(s.loginVerificationOutcome(ctx, user)),
	})
	s.setSessionCookie(ctx, resp.Header(), sessionID, expiresAt)
	return resp, nil
}

func (s *AuthService) BeginPasskeySignUp(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeySignUpRequest]) (*connect.Response[leapmuxv1.BeginPasskeySignUpResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is not available in solo mode"))
	}
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	isSetupMode := !hasUser
	if isSetupMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey sign-up is not available during initial setup; use password sign-up"))
	}
	if !s.signupEnabled(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}

	username, err := validate.SanitizeSlug("username", req.Msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	displayName, err := validate.SanitizeDisplayName(req.Msg.GetDisplayName(), username)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}
	if err := s.validatePublicSignupUsername(ctx, username); err != nil {
		return nil, err
	}
	if err := s.validateSignupEmail(ctx, req.Msg.GetEmail()); err != nil {
		return nil, err
	}
	email := req.Msg.GetEmail()

	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	sessionID, optionsJSON, rpID, err := wa.BeginSignUp(ctx, hubwebauthn.SignupDraft{
		Username:    username,
		Email:       email,
		DisplayName: displayName,
	}, originFromRequest(req))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeySignUpResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

func (s *AuthService) FinishPasskeySignUp(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeySignUpRequest]) (*connect.Response[leapmuxv1.FinishPasskeySignUpResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey sign-up is not available"))
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	draft, cred, err := wa.FinishSignUp(ctx, req.Msg.GetSessionId(), req.Msg.GetCredentialJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Re-check the controls at finish so an admin disable or a race on
	// username/email between Begin and Finish cannot create an account past
	// policy. Setup mode is re-checked too: Begin refuses it, but if every
	// user vanished inside the ceremony window, this commit creates the
	// hub's first account and it must be an admin, exactly like password
	// sign-up -- otherwise /setup is withdrawn and the hub has no
	// administrator.
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	isFirstUser := !hasUser
	if !isFirstUser && !s.signupEnabled(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}
	if err := s.validatePublicSignupUsername(ctx, draft.Username); err != nil {
		return nil, err
	}
	if err := s.validateSignupEmail(ctx, draft.Email); err != nil {
		return nil, err
	}

	email := draft.Email
	pendingEmail := ""
	if s.emailVerificationRequired(ctx) {
		email = ""
		pendingEmail = draft.Email
	}
	createdUser, storedCode, err := createUserInTx(ctx, s.store, createUserTxParams{
		userID:       draft.UserID,
		username:     draft.Username,
		displayName:  draft.DisplayName,
		email:        email,
		pendingEmail: pendingEmail,
		passwordHash: pwdhash.PlaceholderHash,
		isAdmin:      isFirstUser,
		extra: func(tx store.Store) error {
			_, err := wa.StoreCredential(ctx, tx, id.Generate(), draft.UserID, cred, "Passkey")
			return err
		},
	})
	if err != nil {
		return nil, mapSignupCommitError(err)
	}

	// The returned code is the authority on whether a pending verification
	// was actually written, NOT the caller's pre-call intent: createUserInTx
	// promotes an admin's pending address into the email column and writes
	// no pending row, so the first-user branch reaches here with
	// pendingEmail still set and nothing to verify. Sending on the local
	// intent mailed a blank code, and a failed send then rolled back the
	// hub's only administrator.
	verificationRequired := storedCode != ""
	emailSent := false
	var nextResend *time.Time
	if verificationRequired {
		if err := s.deliverSignupVerification(ctx, createdUser.ID, createdUser.PendingEmail, storedCode); err != nil {
			return nil, err
		}
		emailSent = true
		next := nextResendAt(time.Now().UTC())
		nextResend = &next
	}

	s.hasAnyUser.Store(true)
	loginUID, mintErr := mintRowUserID(createdUser.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	sessionID, sessionExpires, err := auth.CreateSession(ctx, s.store, loginUID, s.sessionDuration(ctx))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}
	resp := connect.NewResponse(&leapmuxv1.FinishPasskeySignUpResponse{
		// The ceremony just stored this account's single passkey.
		User: userToProto(createdUser, 1),
		EmailVerification: emailVerificationToProto(verificationOutcome{
			Required:              verificationRequired,
			EmailSent:             emailSent,
			NextResendAvailableAt: nextResend,
		}),
	})
	s.setSessionCookie(ctx, resp.Header(), sessionID, sessionExpires)
	return resp, nil
}

func hashPasswordResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generatePasswordResetToken mints the emailed reset secret from the
// shared id mint (crypto-rand backed, 48 chars over a 62-alphabet, ~285
// bits), mirroring auth.MintAccessSecret and the session ids.
func generatePasswordResetToken() string {
	return id.Generate()
}

func (s *AuthService) RequestPasswordReset(ctx context.Context, req *connect.Request[leapmuxv1.RequestPasswordResetRequest]) (*connect.Response[leapmuxv1.RequestPasswordResetResponse], error) {
	if s.cfg.SoloMode {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	identifier := req.Msg.GetIdentifier()
	var user *store.User
	var err error
	if strings.Contains(identifier, "@") {
		user, err = s.store.Users().GetByEmail(ctx, strings.TrimSpace(identifier))
	} else {
		username, slugErr := validate.SanitizeSlug("username", identifier)
		if slugErr != nil {
			return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
		}
		user, err = s.store.Users().GetByUsername(ctx, username)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Every miss path returns the same empty body as a hit. Timing is NOT
	// equalized: the hit path performs a synchronous SMTP send whose dial
	// dominates any padding, so a burn would be security theater. The real
	// anti-enumeration controls are the captcha on this procedure, the
	// uniform response body, and the per-account resend cooldown below.
	targetEmail := user.Email
	if targetEmail == "" {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	// No IsAdmin exemption here, unlike the auth interceptor's verification
	// control: this predicate asks whether THIS address is trusted with a
	// credential-bearing reset link, and the hub has never verified it. An
	// unverified admin self-recovers via RequestEmailChange + VerifyEmail
	// (both allowlisted for unverified users).
	if !user.EmailVerified && s.emailVerificationRequired(ctx) {
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	// Cooldown: a recent request keeps its link. Minting a fresh token on
	// every request would flood the inbox and invalidate the link the
	// previous email still carries. The mint below is conditional -- the
	// write lands only when no previous token exists or the previous one
	// was issued at least the cooldown ago -- so a double-submit race
	// cannot mint and send twice and the first email's link stays valid.
	// The cutoff derivation: issued_at = previous expiry minus the constant
	// token TTL, so "issued at least cooldown ago" is "previous expiry at
	// or before now + (TTL - cooldown)". Both timestamps are on the app
	// clock, the clock that wrote the expiry.
	cooldownCutoff := time.Now().UTC().Add(passwordResetExpiry - passwordResetResendCooldown)
	rawToken := generatePasswordResetToken()
	expiresAt := time.Now().Add(passwordResetExpiry).UTC()
	minted, err := s.store.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
		ID:                            user.ID,
		PendingPasswordResetToken:     hashPasswordResetToken(rawToken),
		PendingPasswordResetExpiresAt: expiresAt,
		CooldownCutoff:                cooldownCutoff,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !minted {
		// A concurrent or recent request holds a live token: keep it (its
		// email is on the way or already delivered) and answer the same
		// empty body as every other path.
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	if err := s.mail.Send(ctx, s.renderer.PasswordResetEmail(targetEmail, rawToken, passwordResetExpiry)); err != nil {
		// Report the clear separately from the send. A log line that claims
		// "cleared pending token" while the clear itself failed sends the
		// operator looking for the wrong cause of a token that is still
		// live for the full reset TTL.
		if clearErr := s.store.Users().ClearPendingPasswordReset(ctx, user.ID); clearErr != nil {
			slog.WarnContext(ctx, "clear pending password reset after failed send",
				"user_id", user.ID, "err", clearErr)
		}
		slog.WarnContext(ctx, "password reset email send failed; cleared pending token",
			"user_id", user.ID, "err", err)
		// Still return empty success so the response cannot enumerate accounts,
		// but do not leave a live token after a failed send.
		return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
	}
	return connect.NewResponse(&leapmuxv1.RequestPasswordResetResponse{}), nil
}

func (s *AuthService) CompletePasswordReset(ctx context.Context, req *connect.Request[leapmuxv1.CompletePasswordResetRequest]) (*connect.Response[leapmuxv1.CompletePasswordResetResponse], error) {
	if s.cfg.SoloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("password reset is not available in solo mode"))
	}
	token := req.Msg.GetToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token is required"))
	}
	if err := validate.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hashedToken := hashPasswordResetToken(token)

	// Charge one attempt against the row that holds this exact token. The
	// find, the charge, and the token re-check are one statement, so a token
	// cleared by a concurrent reset cannot slip through as a 500.
	charged, err := s.store.Users().ConsumePasswordResetAttemptByToken(ctx, hashedToken, time.Now().UTC(), maxPasswordResetAttempts)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Attempt 6+ expires the token in SQL (sets expires_at = now). Refuse
	// before Argon2 so the attempt budget is a hard cap, not soft.
	if charged.PendingPasswordResetAttempts > maxPasswordResetAttempts {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
	}
	if charged.PendingPasswordResetExpiresAt != nil && time.Now().UTC().After(*charged.PendingPasswordResetExpiresAt) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reset token expired"))
	}

	hashed, err := pwdhash.Hash(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	user := charged
	uid, mintErr := mintRowUserID(user.ID)
	if mintErr != nil {
		return nil, mintErr
	}
	var revoked *store.PasswordResetRevocation
	if err := s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		var completeErr error
		revoked, completeErr = tx.Users().CompletePasswordReset(ctx, store.CompletePasswordResetParams{
			ID:                        user.ID,
			PasswordHash:              hashed,
			PendingPasswordResetToken: hashedToken,
		})
		if completeErr != nil {
			if errors.Is(completeErr, store.ErrNotFound) {
				return connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired reset token"))
			}
			return completeErr
		}
		if err := RevokePasskeyAuthState(ctx, tx, user.ID); err != nil {
			return err
		}
		if err := tx.Sessions().DeleteByUser(ctx, uid); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		if _, err := tx.APITokens().RevokeByUser(ctx, uid); err != nil {
			return fmt.Errorf("revoke api tokens: %w", err)
		}
		if _, err := tx.DelegationTokens().RevokeByUser(ctx, uid); err != nil {
			return fmt.Errorf("revoke delegation tokens: %w", err)
		}
		return nil
	}); err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if revoked != nil {
		s.lifecycle.UserRevoked(user.ID, revoked.AuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.CompletePasswordResetResponse{}), nil
}

// RevokePasskeyAuthState removes every passkey artifact a user owns: the
// credential rows, their ceremony and proof sessions, and any pending
// password reset. The credential-rotation teardown paths (self-service
// CompletePasswordReset, admin ResetPassword, admin DeleteUser, the
// offline recover CLI, and signup rollback) share it, so the next
// credential type is registered here once instead of remembered at each
// rotation site.
func RevokePasskeyAuthState(ctx context.Context, tx store.Store, userID string) error {
	if err := tx.PasskeyCredentials().DeleteAllByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete passkeys: %w", err)
	}
	if err := tx.WebAuthnSessions().DeleteAllByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete webauthn sessions: %w", err)
	}
	if err := tx.Users().ClearPendingPasswordReset(ctx, userID); err != nil {
		return fmt.Errorf("clear pending password reset: %w", err)
	}
	return nil
}

// rollbackUnusableSignup removes a just-created account after a fail-closed
// verification email failure that happened outside the create transaction.
// All steps share one transaction so a partial wipe cannot leave a
// passkey-less account that still holds the username.
func rollbackUnusableSignup(ctx context.Context, st store.Store, userID string) error {
	return st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := RevokePasskeyAuthState(ctx, tx, userID); err != nil {
			return err
		}
		if uid, ok := userid.New(userID); ok {
			links, err := tx.OAuthUserLinks().ListByUser(ctx, uid)
			if err != nil {
				return fmt.Errorf("list oauth links: %w", err)
			}
			for _, link := range links {
				if err := tx.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
					UserID:     uid,
					ProviderID: link.ProviderID,
				}); err != nil {
					return fmt.Errorf("delete oauth link: %w", err)
				}
			}
		}
		if err := tx.Users().Delete(ctx, userID); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		return nil
	})
}
