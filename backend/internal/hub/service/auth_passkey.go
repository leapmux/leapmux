package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

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
	sessionID, optionsJSON, err := wa.BeginLogin(ctx, user.ID, originFromRequest(req))
	if err != nil {
		if classifyWebAuthnError(err) == webAuthnErrorUnavailable {
			// An unserved origin gives the remediation. Everything else in
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
			return nil, credentialRejectedError(err)
		case webAuthnErrorCredential:
			// The marker for the same reason the management surface carries
			// it: the rejected credential is the assertion the REQUEST
			// carried. It changes nothing on this endpoint today -- a visitor
			// at /login holds no session to end -- and it is here so the rule
			// is "a rejected credential always says so" rather than "except
			// on the endpoints nobody checked".
			return nil, credentialRejectedError(err)
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
	if err := rejectSolo(s.cfg.SoloMode, "sign-up"); err != nil {
		return nil, err
	}
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	isSetupMode := !hasUser
	// The signup setting is an administrator's decision, and setup mode is
	// the state in which no administrator exists to have made it -- the same
	// exemption password SignUp takes. Everything the first administrator
	// needs is already here: FinishPasskeySignUp creates the first account as
	// an admin and promotes its address into the email column, exactly like
	// signUpSetupMode.
	if !isSetupMode && !s.signupEnabled(ctx) {
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
	if err := s.validateSignupUsername(ctx, username, isSetupMode); err != nil {
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
	sessionID, optionsJSON, err := wa.BeginSignUp(ctx, hubwebauthn.SignupDraft{
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
	// policy. This handler re-checks setup mode rather than carries it over
	// from Begin, because the state can flip either way inside the ceremony
	// window: a second operator can win the race to become the first
	// administrator, and every user can vanish. This commit creates the hub's
	// first account whenever it is still the first, and that account must be
	// an admin, exactly like password sign-up -- otherwise the hub withdraws
	// /setup and has no administrator.
	hasUser, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
	}
	isFirstUser := !hasUser
	if !isFirstUser && !s.signupEnabled(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sign-up is disabled"))
	}
	if err := s.validateSignupUsername(ctx, draft.Username, isFirstUser); err != nil {
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
		now:          s.now,
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
		next := pendingResendDeadline(s.now(), createdUser.PendingEmailUnblockedAt)
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

// RevokePasskeyAuthState removes every passkey artifact a user owns: the
// credential rows, their in-flight ceremony sessions, and any pending
// account recovery. The credential-rotation teardown paths (self-service
// CompleteAccountRecoveryPassword, admin ResetPassword, admin DeleteUser, the
// offline recover CLI, and signup rollback) share it, so the next
// credential type needs one registration here instead of one at each
// rotation site.
//
// There is no separate step-up artifact to sweep. The single-use reauth
// proof this replaced had its own webauthn_sessions kind; a session
// ELEVATION is a pair of columns on user_sessions instead, and every caller
// here deletes the account's session rows in the same transaction, so the
// window dies with the row that carried it.
func RevokePasskeyAuthState(ctx context.Context, tx store.Store, userID string) error {
	if err := tx.PasskeyCredentials().DeleteAllByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete passkeys: %w", err)
	}
	if err := tx.WebAuthnSessions().DeleteAllByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete webauthn sessions: %w", err)
	}
	if err := tx.Users().ClearPendingRecovery(ctx, store.ClearPendingRecoveryParams{
		ID: userID,
		// The rotation killed the flow the link rode; the account's next
		// request must be free to mint, so the clear leaves no blockade
		// (the zero time binds NULL).
		UnblockedAt: time.Time{},
	}); err != nil {
		return fmt.Errorf("clear pending recovery: %w", err)
	}
	return nil
}

// RevokeCredentialsAfterRotation ends every bearer a credential rotation
// invalidates, inside the caller's user-auth transaction: the account's
// sessions, api tokens, and delegation tokens. The rotation sites --
// self-service CompleteAccountRecoveryPassword, admin ResetPassword, admin
// DeleteUser, the offline recover CLI, and admin RevokeUserSessions --
// pair it with RevokePasskeyAuthState (except RevokeUserSessions, which
// rotates no passkey), so the next credential kind registers in one place
// instead of at each rotation site.
//
// generationAlreadyBumped is true only on the recovery completion:
// CompleteRecovery's row update already moved tokens_revoked_at and
// auth_generation and returned the committed generation, so the bearer
// branches revoke rows directly -- auth.RevokeAllUserCredentials would bump
// the epoch a second time and target an epoch the caller does not hold.
//
// Only the false branch writes the DURABLE revocation event (inside
// auth.RevokeAllUserCredentials): the true branch's direct row revocations
// emit none, and CompleteRecovery's UPDATE does not either, so on the
// recovery path the revocation reaches other processes through the
// committed generation bump -- a foreign replica's cached auth context
// revalidates against the row -- and not through the revocation-events
// stream.
//
// It does only the durable half. The caller owns the other half and must,
// AFTER the commit, apply `lifecycle.UserRevoked(userID, committedGeneration)`
// -- auth.RevokeAllUserCredentials requires an in-process caller to evict
// the auth-context registry itself, because the durable event reaches this
// hub only on the revocation watcher's next sweep, up to two seconds later.
// The effect stays at the call site because it cannot run from in here: a
// rollback after it would evict credentials that were never revoked.
//
// It takes the minted userid.UserID alone and spells the row id from it, so
// no caller can pass an id and a userid that identify two different users.
func RevokeCredentialsAfterRotation(
	ctx context.Context, tx store.Store, uid userid.UserID, generationAlreadyBumped bool,
) (apiCount, delegationCount, committedGeneration int64, err error) {
	if err := tx.Sessions().DeleteByUser(ctx, uid); err != nil {
		return 0, 0, 0, fmt.Errorf("delete sessions: %w", err)
	}
	if generationAlreadyBumped {
		if apiCount, err = tx.APITokens().RevokeByUser(ctx, uid); err != nil {
			return 0, 0, 0, fmt.Errorf("revoke api tokens: %w", err)
		}
		if delegationCount, err = tx.DelegationTokens().RevokeByUser(ctx, uid); err != nil {
			return 0, 0, 0, fmt.Errorf("revoke delegation tokens: %w", err)
		}
	} else if apiCount, delegationCount, err = auth.RevokeAllUserCredentials(ctx, tx, uid); err != nil {
		return 0, 0, 0, err
	}
	// The committed epoch, read INSIDE the transaction, so the post-commit
	// eviction targets exactly what this commit revoked. The include-deleted
	// form reads the fenced row whatever its deleted_at holds, so a caller
	// that also soft-deletes the user still gets an epoch back.
	revoked, err := tx.Users().GetByIDIncludeDeleted(ctx, uid.String())
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query revoked user auth generation: %w", err)
	}
	return apiCount, delegationCount, revoked.AuthGeneration, nil
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
