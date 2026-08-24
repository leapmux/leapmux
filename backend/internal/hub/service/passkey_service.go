package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

func rejectSoloPasskeyManagement(solo bool) error {
	if !solo {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("passkey management is not available in solo mode"))
}

func invalidReauthProofError() error {
	return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("%w", auth.ErrInvalidReauthProof))
}

// verifyPasswordForPasskeyManagement checks the current password when the
// account has one. Wrong password returns ErrInvalidCurrentPassword so the
// rate-limit interceptor can count the failure.
func verifyPasswordForPasskeyManagement(user *store.User, currentPassword string) error {
	if currentPassword == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("%w", auth.ErrInvalidCurrentPassword))
	}
	match, err := password.Verify(user.PasswordHash, currentPassword)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", err))
	}
	if !match {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("%w", auth.ErrInvalidCurrentPassword))
	}
	return nil
}

// passkeyManagementAuth verifies the step-up credential OUTSIDE the
// user-auth transaction: password verification runs Argon2 (tens of
// milliseconds) and that transaction holds the database writer lock on
// SQLite (see auth.Login's comment on the same trade). It returns whether
// the caller must consume a reauth proof after its mutation succeeds; the
// proof is only consumed inside the mutation transaction, and only then.
func (s *UserService) passkeyManagementAuth(ctx context.Context, user *store.User, currentPassword, reauthProof string) (bool, error) {
	if user.PasswordSet {
		return false, verifyPasswordForPasskeyManagement(user, currentPassword)
	}
	count, err := s.store.PasskeyCredentials().CountByUser(ctx, user.ID)
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, err)
	}
	if count == 0 {
		return false, assertFirstCredentialWithoutPasswordAllowed(ctx, s.store, user)
	}
	if reauthProof == "" {
		return true, invalidReauthProofError()
	}
	// Peek with the same validity predicate the consume uses, so an
	// admission check and the consume cannot disagree on what a valid
	// proof is.
	if _, err := s.store.WebAuthnSessions().GetValidProof(ctx, reauthProof, user.ID, hubwebauthn.KindReauthProof, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true, invalidReauthProofError()
		}
		return true, connect.NewError(connect.CodeInternal, err)
	}
	return true, nil
}

// assertFirstCredentialWithoutPasswordAllowed refuses a session-only first
// lasting credential (a passkey, or the first password) when the account has
// no durable identity (verified email or OAuth link). A stolen session on an
// unverified shell must not attach a credential it can keep.
func assertFirstCredentialWithoutPasswordAllowed(ctx context.Context, tx store.Store, user *store.User) error {
	if user.PasswordSet {
		return nil
	}
	count, err := tx.PasskeyCredentials().CountByUser(ctx, user.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if count > 0 {
		return nil
	}
	if user.EmailVerified {
		return nil
	}
	uid, ok := userid.New(user.ID)
	if !ok {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("invalid user id"))
	}
	links, err := tx.OAuthUserLinks().ListByUser(ctx, uid)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if len(links) > 0 {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("verify your email or link an OAuth provider before adding a passkey"))
}

// consumePasskeyManagementReauth deletes a single-use reauth proof after a
// successful mutation. Call only when passkeyManagementAuth returned
// needsReauth=true. Password accounts never reach this path.
func consumePasskeyManagementReauth(ctx context.Context, tx store.Store, user *store.User, reauthProof string) error {
	if reauthProof == "" {
		return invalidReauthProofError()
	}
	n, err := tx.WebAuthnSessions().ConsumeProof(ctx, reauthProof, user.ID, hubwebauthn.KindReauthProof, time.Now().UTC())
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if n == 0 {
		return invalidReauthProofError()
	}
	return nil
}

// runPasskeyManagementTx admits and runs a passkey-management mutation
// (Finish/Rename/Delete/Deactivate, and ChangePassword's step-up side). The
// step-up credential is verified OUTSIDE the user-auth transaction: password
// verification runs Argon2 (tens of milliseconds) and that transaction holds
// the database writer lock on SQLite (see auth.Login's comment on the same
// trade). prepare runs after admission and still outside the lock -- callers
// hash a new password there. Inside the transaction the user row is re-read
// and the step-up is re-checked when the credential state moved between the
// peek and the lock, mirroring auth.Login's prelock-verify/locked-recheck
// pattern: a password rotated concurrently must not authorize this mutation
// with the credential the rotation replaced. The reauth proof, when one was
// required, is consumed inside the transaction after mutate succeeds -- the
// only place a single-use proof may die.
func (s *UserService) runPasskeyManagementTx(
	ctx context.Context,
	userInfo *auth.UserInfo,
	currentPassword, reauthProof string,
	prepare func(peek *store.User) error,
	mutate func(tx store.Store, user *store.User) error,
) error {
	peek, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}
	needsReauth, err := s.passkeyManagementAuth(ctx, peek, currentPassword, reauthProof)
	if err != nil {
		return err
	}
	if prepare != nil {
		if err := prepare(peek); err != nil {
			return err
		}
	}
	return s.store.RunInUserAuthTransaction(ctx, userInfo.ID, func(tx store.Store) error {
		user, err := tx.Users().GetByID(ctx, userInfo.ID.String())
		if err != nil {
			return fmt.Errorf("query user: %w", err)
		}
		if err := recheckStepUpUnderLock(user, peek, currentPassword); err != nil {
			return err
		}
		if err := mutate(tx, user); err != nil {
			return err
		}
		if needsReauth {
			return consumePasskeyManagementReauth(ctx, tx, user, reauthProof)
		}
		return nil
	})
}

// recheckStepUpUnderLock re-verifies the step-up credential when the
// account's password state moved between the pre-transaction peek and the
// locked re-read. The common case (nothing moved) is one string comparison,
// so Argon2 stays out of the lock unless the hash actually changed. A
// structural flip (password added or removed concurrently) cannot be
// re-verified from what the caller presented, so it fails with a clean
// retry error instead.
func recheckStepUpUnderLock(locked, peek *store.User, currentPassword string) error {
	if locked.PasswordSet != peek.PasswordSet {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("account credentials changed; please retry"))
	}
	if locked.PasswordSet && locked.PasswordHash != peek.PasswordHash {
		return verifyPasswordForPasskeyManagement(locked, currentPassword)
	}
	return nil
}

func (s *UserService) BeginPasskeyRegistration(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeyRegistrationRequest]) (*connect.Response[leapmuxv1.BeginPasskeyRegistrationResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Peek only: FinishPasskeyRegistration consumes the reauth proof.
	if _, err := s.passkeyManagementAuth(ctx, user, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof()); err != nil {
		return nil, mapPasskeyConnectError(err)
	}

	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	sessionID, optionsJSON, rpID, err := wa.BeginRegistration(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyRegistrationResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

func (s *UserService) FinishPasskeyRegistration(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyRegistrationRequest]) (*connect.Response[leapmuxv1.FinishPasskeyRegistrationResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_id is required"))
	}
	if req.Msg.GetCredentialJson() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential_json is required"))
	}
	friendlyName, err := validatePasskeyFriendlyName(req.Msg.GetFriendlyName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var passkey *store.PasskeyCredential
	// Peek before the attestation so a failed attestation does not burn the
	// proof; the proof is consumed inside the transaction after the write.
	if err := s.runPasskeyManagementTx(ctx, userInfo, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof(), nil, func(tx store.Store, user *store.User) error {
		wa, err := s.webauthnServiceWithStore(ctx, tx)
		if err != nil {
			return err
		}
		passkey, err = wa.FinishRegistration(ctx, user.ID, req.Msg.GetSessionId(), req.Msg.GetCredentialJson(), friendlyName)
		return err
	}); err != nil {
		return nil, mapPasskeyConnectError(err)
	}

	return connect.NewResponse(&leapmuxv1.FinishPasskeyRegistrationResponse{
		Passkey: passkeyInfoToProto(passkey),
	}), nil
}

func (s *UserService) BeginPasskeyReauth(ctx context.Context, req *connect.Request[leapmuxv1.BeginPasskeyReauthRequest]) (*connect.Response[leapmuxv1.BeginPasskeyReauthResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	sessionID, optionsJSON, rpID, err := wa.BeginReauth(ctx, user.ID, originFromRequest(req))
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	return connect.NewResponse(&leapmuxv1.BeginPasskeyReauthResponse{
		SessionId:   sessionID,
		OptionsJson: optionsJSON,
		RpId:        rpID,
	}), nil
}

func (s *UserService) FinishPasskeyReauth(ctx context.Context, req *connect.Request[leapmuxv1.FinishPasskeyReauthRequest]) (*connect.Response[leapmuxv1.FinishPasskeyReauthResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_id is required"))
	}
	if req.Msg.GetCredentialJson() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential_json is required"))
	}

	user, err := s.store.Users().GetByID(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	proof, err := wa.FinishReauth(ctx, user.ID, req.Msg.GetSessionId(), req.Msg.GetCredentialJson())
	if err != nil {
		switch {
		case errors.Is(err, hubwebauthn.ErrCloneDetected):
			// A clone warning is a security event, not a proof failure: log
			// it server-side and report it as itself, so it never counts
			// against the reauth-proof rate-limit budget.
			slog.WarnContext(ctx, "passkey clone warning during reauth", "user_id", user.ID)
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		case errors.Is(err, hubwebauthn.ErrCeremonyInvalid), errors.Is(err, hubwebauthn.ErrAssertionRejected):
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("%w: %s", auth.ErrInvalidReauthProof, err.Error()))
		default:
			// Store and infrastructure failures are not proof failures.
			// Reporting them as CodeInternal keeps them out of the shared
			// passkey-management rate-limit budget.
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&leapmuxv1.FinishPasskeyReauthResponse{ReauthProof: proof}), nil
}

func (s *UserService) ListPasskeys(ctx context.Context, _ *connect.Request[leapmuxv1.ListPasskeysRequest]) (*connect.Response[leapmuxv1.ListPasskeysResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.PasskeyCredentials().ListByUser(ctx, userInfo.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Surface an unconfigured hub instead of hiding it behind an empty
	// list: the client cannot tell "no passkeys" from "passkeys cannot run
	// here", and the settings page would render a broken section.
	wa, err := s.webauthnService(ctx)
	if err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	passkeys := make([]*leapmuxv1.PasskeyInfo, 0, len(rows))
	for i := range rows {
		passkeys = append(passkeys, passkeyInfoToProto(&rows[i]))
	}
	return connect.NewResponse(&leapmuxv1.ListPasskeysResponse{Passkeys: passkeys, RpId: wa.RPID()}), nil
}

func (s *UserService) RenamePasskey(ctx context.Context, req *connect.Request[leapmuxv1.RenamePasskeyRequest]) (*connect.Response[leapmuxv1.RenamePasskeyResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	friendlyName, err := validatePasskeyFriendlyName(req.Msg.GetFriendlyName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var row *store.PasskeyCredential
	if err := s.runPasskeyManagementTx(ctx, userInfo, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof(), nil, func(tx store.Store, user *store.User) error {
		got, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return err
		}
		if got.UserID != user.ID {
			return store.ErrNotFound
		}
		if err := tx.PasskeyCredentials().UpdateFriendlyName(ctx, got.ID, got.UserID, friendlyName); err != nil {
			return err
		}
		got.FriendlyName = friendlyName
		row = got
		return nil
	}); err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	return connect.NewResponse(&leapmuxv1.RenamePasskeyResponse{
		Passkey: passkeyInfoToProto(row),
	}), nil
}

func (s *UserService) DeletePasskey(ctx context.Context, req *connect.Request[leapmuxv1.DeletePasskeyRequest]) (*connect.Response[leapmuxv1.DeletePasskeyResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}

	actingSessionID := userInfo.Credential.SessionID()
	var committedAuthGeneration int64
	var shouldRevoke bool
	// Hash the replacement password outside the transaction (Argon2 must
	// not hold the SQLite writer lock). Only the last-passkey branch on a
	// passwordless account needs one; the transaction re-derives that
	// decision from the locked row and refuses an empty hash.
	var hashedNewPassword string
	prepare := func(peek *store.User) error {
		if peek.PasswordSet {
			return nil
		}
		count, err := s.store.PasskeyCredentials().CountByUser(ctx, peek.ID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if count > 1 {
			return nil
		}
		if req.Msg.GetNewPassword() == "" {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete your only passkey; provide new_password or use DeactivatePasskeyAuth"))
		}
		hashed, err := hashReplacementPassword(req.Msg.GetNewPassword())
		if err != nil {
			return err
		}
		hashedNewPassword = hashed
		return nil
	}
	if err := s.runPasskeyManagementTx(ctx, userInfo, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof(), prepare, func(tx store.Store, user *store.User) error {
		row, err := tx.PasskeyCredentials().GetByID(ctx, req.Msg.GetId())
		if err != nil {
			return err
		}
		if row.UserID != user.ID {
			return store.ErrNotFound
		}
		count, err := tx.PasskeyCredentials().CountByUser(ctx, user.ID)
		if err != nil {
			return err
		}
		// Last passkey on a passkey-only account: delegate to the
		// deactivation commit (plan CommitPasskeyDelete).
		if !user.PasswordSet && count <= 1 {
			gen, revoked, err := s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, actingSessionID)
			if err != nil {
				return err
			}
			committedAuthGeneration = gen
			shouldRevoke = revoked
			return nil
		}
		return tx.PasskeyCredentials().Delete(ctx, row.ID, user.ID)
	}); err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	if shouldRevoke {
		s.lifecycle.RevokeUserPreservingSession(userInfo.ID.String(), actingSessionID, committedAuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.DeletePasskeyResponse{}), nil
}

func (s *UserService) DeactivatePasskeyAuth(ctx context.Context, req *connect.Request[leapmuxv1.DeactivatePasskeyAuthRequest]) (*connect.Response[leapmuxv1.DeactivatePasskeyAuthResponse], error) {
	if err := rejectSoloPasskeyManagement(s.cfg.SoloMode); err != nil {
		return nil, err
	}
	userInfo, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	actingSessionID := userInfo.Credential.SessionID()
	var committedAuthGeneration int64
	var shouldRevoke bool
	// Hash the replacement password outside the transaction (Argon2 must
	// not hold the SQLite writer lock). Only a passwordless account needs
	// one; the transaction re-derives that from the locked row.
	var hashedNewPassword string
	prepare := func(peek *store.User) error {
		if peek.PasswordSet {
			return nil
		}
		hashed, err := hashReplacementPassword(req.Msg.GetNewPassword())
		if err != nil {
			return err
		}
		hashedNewPassword = hashed
		return nil
	}
	if err := s.runPasskeyManagementTx(ctx, userInfo, req.Msg.GetCurrentPassword(), req.Msg.GetReauthProof(), prepare, func(tx store.Store, user *store.User) error {
		gen, revoked, err := s.commitPasskeyDeactivation(ctx, tx, user, hashedNewPassword, actingSessionID)
		if err != nil {
			return err
		}
		committedAuthGeneration = gen
		shouldRevoke = revoked
		return nil
	}); err != nil {
		return nil, mapPasskeyConnectError(err)
	}
	if shouldRevoke {
		s.lifecycle.RevokeUserPreservingSession(userInfo.ID.String(), actingSessionID, committedAuthGeneration)
	}
	return connect.NewResponse(&leapmuxv1.DeactivatePasskeyAuthResponse{}), nil
}

// hashReplacementPassword validates and hashes a replacement password for
// the passkey-deactivation paths. Call outside the user-auth transaction:
// Argon2 must not hold the SQLite writer lock.
func hashReplacementPassword(newPassword string) (string, error) {
	if err := validate.ValidatePassword(newPassword); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	hashed, err := password.Hash(newPassword)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}
	return hashed, nil
}

// commitPasskeyDeactivation deletes every passkey for the user. On a
// passkey-only account it also sets the pre-hashed replacement password
// (hashedNewPassword; empty means the caller did not supply one) and
// revokes other sessions and tokens while preserving the acting session
// (mirror ChangePassword). Caller must hold the user-auth lock and must
// verify auth before this call.
func (s *UserService) commitPasskeyDeactivation(ctx context.Context, tx store.Store, user *store.User, hashedNewPassword, actingSessionID string) (committedAuthGeneration int64, revoked bool, err error) {
	wasPasskeyOnly := !user.PasswordSet
	if wasPasskeyOnly {
		if hashedNewPassword == "" {
			return 0, false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete your only passkey; provide new_password or use DeactivatePasskeyAuth"))
		}
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashedNewPassword,
			ID:           user.ID,
		}); err != nil {
			return 0, false, fmt.Errorf("update password: %w", err)
		}
	}
	if err := tx.PasskeyCredentials().DeleteAllByUser(ctx, user.ID); err != nil {
		return 0, false, err
	}
	if !wasPasskeyOnly {
		return 0, false, nil
	}
	gen, err := s.revokeOtherCredentialsPreservingSession(ctx, tx, user.ID, actingSessionID)
	if err != nil {
		return 0, false, err
	}
	return gen, true, nil
}

// revokeOtherCredentialsPreservingSession deletes other sessions, revokes API
// and delegation tokens, bumps auth_generation, and restamps the acting
// session. Caller must hold the user-auth transaction. Shared by
// ChangePassword, DeletePasskey, and DeactivatePasskeyAuth.
//
// RefreshAuthGeneration returning n==0 means the acting session was
// concurrently deleted (a same-user logout does not contend on this
// user-auth lock) after the transaction began. The password change itself
// stays valid and there is no surviving session row left to restamp, so the
// caller does not roll the change back; the post-transaction revocation is
// a harmless no-op for a same-process logout and self-heals across hubs
// once the durable session-revoked event replays. n>1 is impossible
// (session id is unique) and indicates corruption, so it stays fatal.
func (s *UserService) revokeOtherCredentialsPreservingSession(ctx context.Context, tx store.Store, userID, actingSessionID string) (int64, error) {
	rowUID, err := mintRowUserID(userID)
	if err != nil {
		return 0, err
	}
	if err := tx.Sessions().DeleteOthers(ctx, store.DeleteOtherSessionsParams{
		UserID: rowUID,
		KeepID: actingSessionID,
	}); err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	if _, _, err := auth.RevokeAllUserCredentials(ctx, tx, rowUID); err != nil {
		return 0, err
	}
	if actingSessionID != "" {
		n, err := tx.Sessions().RefreshAuthGeneration(ctx, store.RefreshSessionAuthGenerationParams{
			SessionID: actingSessionID,
			UserID:    rowUID,
		})
		if err != nil {
			return 0, fmt.Errorf("refresh current session auth generation: %w", err)
		}
		if n > 1 {
			return 0, fmt.Errorf("refresh current session auth generation: updated %d rows", n)
		}
	}
	updatedUser, err := tx.Users().GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("query updated user auth generation: %w", err)
	}
	return updatedUser.AuthGeneration, nil
}

func validatePasskeyFriendlyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Passkey", nil
	}
	// Count characters, not bytes: a CJK or emoji name under 64 characters
	// is valid input and must not be rejected by a message about characters.
	if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("friendly name must be at most 64 characters")
	}
	return name, nil
}

func passkeyInfoToProto(row *store.PasskeyCredential) *leapmuxv1.PasskeyInfo {
	if row == nil {
		return nil
	}
	info := &leapmuxv1.PasskeyInfo{
		Id:           row.ID,
		FriendlyName: row.FriendlyName,
		CreatedAt:    timestamppb.New(row.CreatedAt),
		// Base64url credential id for the browser Signal API after delete.
		CredentialId: base64.RawURLEncoding.EncodeToString(row.CredentialID),
	}
	if row.LastUsedAt != nil {
		info.LastUsedAt = timestamppb.New(*row.LastUsedAt)
	}
	transports, _ := parsePasskeyTransports(row.Transports)
	info.Transports = transports
	return info
}

func parsePasskeyTransports(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var transports []string
	if err := json.Unmarshal([]byte(raw), &transports); err != nil {
		return nil, fmt.Errorf("parse passkey transports: %w", err)
	}
	return transports, nil
}

func mapPasskeyConnectError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	if errors.Is(err, ErrPasskeysUnavailable) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
