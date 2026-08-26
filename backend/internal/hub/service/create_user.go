package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/sections"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// pendingEmailExpiry is the lifetime of a freshly issued verification
// code. 30 minutes is long enough for the recipient to switch to their
// inbox and back, but short enough that a leaked code becomes useless
// well before someone could try to brute-force it.
const pendingEmailExpiry = 30 * time.Minute

// maxVerificationAttempts is the per-code attempt budget. The 6th wrong
// guess force-expires the code. With a 31-character alphabet this caps
// the success probability of a remote brute-force at 5/31^6 ≈ 5e-9. The
// SQL force-expire binds this same constant (sqlc.arg(max_attempts)), so
// the Go gate and the store can never disagree at the boundary attempt.
const maxVerificationAttempts = 5

// maxPasswordResetAttempts is the per-token attempt budget for the
// self-service password reset. The token is a 285-bit secret, so the
// budget is defense-in-depth against a throttled oracle, not a
// brute-force bound; the SQL force-expire binds the same constant.
const maxPasswordResetAttempts = 5

// CreateUserParams holds the parameters for creating a new user.
type CreateUserParams struct {
	Username      string
	PasswordHash  string
	DisplayName   string
	Email         string
	EmailVerified bool
	PasswordSet   bool
	IsAdmin       bool
}

// createUserTxParams describes one account creation. Extra runs inside the
// same transaction after the user row exists, so a flavor-specific artifact
// (a passkey credential, an OAuth link) commits with the account.
type createUserTxParams struct {
	userID        string // empty: generated here
	username      string
	displayName   string
	email         string // direct email column value
	emailVerified bool
	pendingEmail  string // non-empty: stored as the pending verification row
	passwordHash  string
	passwordSet   bool
	isAdmin       bool
	extra         func(tx store.Store) error
}

// createUserInTx creates the account, seeds the default sidebar sections,
// runs the flavor hook, and stores the pending verification row, all in one
// transaction. It returns the created user row and, when a pending email
// was stored, the verification code. This one routine serves the admin
// CreateUser verb and the password, OAuth, and passkey sign-up flavors, so
// the create-user write shape and the user-always-has-sections invariant
// cannot drift between them.
//
// Every account-creation invariant belongs HERE, not in one caller. A rule
// that a caller applies is a rule the next flavor omits: the admin
// pending-email promotion and the competing-pending-email clear both lived
// in CreateUser, and the OAuth and passkey sign-ups that call this routine
// directly went past both.
func createUserInTx(ctx context.Context, st store.Store, opt createUserTxParams) (*store.User, string, error) {
	// An admin never waits behind a pending verification row: the address
	// moves to the email column instead, which is what /setup already does
	// through signUpSetupMode. Nothing would ever prompt them to supply one,
	// because loginVerificationOutcome short-circuits on IsAdmin.
	//
	// email_verified is NOT forced here, and the difference matters. The
	// column says whether somebody CONFIRMED this address, and that is a
	// fact about the address rather than a privilege of the account. Forcing
	// it made an administrator's unconfirmed address a valid self-service
	// password-reset target, because RequestPasswordReset reads the column
	// and has no admin exemption -- and it cannot have one, since the
	// question it asks is exactly "has anybody confirmed this address".
	//
	// What the force protected is the LOGIN gate, and that gate derives the
	// exemption for itself: the auth interceptor and both passkey login legs
	// call auth.EmailVerificationSatisfied, so an administrator is never
	// locked out by an unconfirmed address.
	if opt.isAdmin && opt.email == "" && opt.pendingEmail != "" {
		opt.email, opt.pendingEmail = opt.pendingEmail, ""
	}

	var user *store.User
	var code string
	err := st.RunInTransaction(ctx, func(tx store.Store) error {
		userID := opt.userID
		if userID == "" {
			userID = id.Generate()
		}
		sectionOwner, ok := userid.New(userID)
		if !ok {
			return fmt.Errorf("generated empty user id")
		}
		if err := tx.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			Username:      opt.username,
			PasswordHash:  opt.passwordHash,
			DisplayName:   opt.displayName,
			Email:         opt.email,
			EmailVerified: opt.emailVerified,
			PasswordSet:   opt.passwordSet,
			IsAdmin:       opt.isAdmin,
		}); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if err := sections.InitDefaults(ctx, tx, sectionOwner); err != nil {
			return fmt.Errorf("init sections: %w", err)
		}
		if opt.extra != nil {
			if err := opt.extra(tx); err != nil {
				return err
			}
		}
		if opt.pendingEmail != "" {
			code = verifycode.Generate()
			expiresAt := time.Now().Add(pendingEmailExpiry).UTC()
			// A brand-new account holds no previous code, so the
			// conditional mint always lands; UnconditionalMintCutoff says that
			// explicitly rather than leaving a zero cutoff to mean it.
			if _, err := tx.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
				ID:                    userID,
				PendingEmail:          opt.pendingEmail,
				PendingEmailToken:     code,
				PendingEmailExpiresAt: &expiresAt,
				CooldownCutoff:        store.UnconditionalMintCutoff(),
			}); err != nil {
				return fmt.Errorf("set pending email: %w", err)
			}
		}
		// The account claimed this address, so no other user may keep it as
		// a pending verification target. Inside the transaction, so the
		// claim and the clear commit together.
		ClearCompetingPendingEmails(ctx, tx, opt.email, userID)
		u, err := tx.Users().GetByID(ctx, userID)
		if err != nil {
			return err
		}
		user = u
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return user, code, nil
}

// CreateUser creates a user and seeds their default sidebar sections. It
// returns the created user row.
//
// The write itself lives in createUserInTx, the one transaction shared by
// every account-creation flavor: a user can never exist without the
// sections. That is what lets ListSections stay a pure read: seeding them
// there instead was a read-modify-write that nothing serialized, so two
// concurrent reads for one user each saw an empty list and each wrote a full
// set, and the sidebar rendered two of every section.
func CreateUser(ctx context.Context, st store.Store, p CreateUserParams) (*store.User, error) {
	// The admin pending-email promotion and the competing-pending-email
	// clear both live in createUserInTx, so every sign-up flavor gets them.
	user, _, err := createUserInTx(ctx, st, createUserTxParams{
		username:      p.Username,
		displayName:   p.DisplayName,
		email:         p.Email,
		emailVerified: p.EmailVerified,
		passwordHash:  p.PasswordHash,
		passwordSet:   p.PasswordSet,
		isAdmin:       p.IsAdmin,
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// SetEmailAndClearCompeting updates a user's email and clears competing
// pending_email entries from other users. Use this instead of calling
// UpdateUserEmail + ClearCompetingPendingEmails separately.
func SetEmailAndClearCompeting(ctx context.Context, st store.Store, userID, email string, verified bool) error {
	if err := st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
		ID:            userID,
		Email:         email,
		EmailVerified: verified,
	}); err != nil {
		return err
	}
	ClearCompetingPendingEmails(ctx, st, email, userID)
	return nil
}

// CheckEmailAvailable checks that no other user has the given email in their
// verified email column. Empty emails are always allowed. Use excludeUserID
// to skip the current user (for email changes).
//
// Multiple users may have the same pending_email concurrently — only the
// verified email column is checked here. When a user promotes their
// pending_email, promotePendingEmail clears competing pending_email entries.
func CheckEmailAvailable(ctx context.Context, st store.Store, email, excludeUserID string) error {
	if email == "" {
		return nil
	}
	taken, err := st.Users().ExistsByEmail(ctx, email, excludeUserID)
	if err != nil {
		return fmt.Errorf("check email: %w", err)
	}
	if taken {
		return &FieldTakenError{Field: "email", Value: email}
	}
	return nil
}

// CheckUsernameAvailable checks that no other user has the given username. A
// blank username is skipped: `admin user update --email` changes only the
// email, so an unchanged (unsupplied) username must not be reported as a cause.
func CheckUsernameAvailable(ctx context.Context, st store.Store, username string) error {
	if username == "" {
		return nil
	}
	taken, err := st.Users().ExistsByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("check username: %w", err)
	}
	if taken {
		return &FieldTakenError{Field: "username", Value: username}
	}
	return nil
}

// FieldTakenError names the field a create/update collided on, so each caller
// can render it in its own idiom: a connect AlreadyExists for RPC callers, a
// plain sentence for the admin CLI.
//
// It replaces three separate implementations of one uniqueness rule -- this
// pair plus the admin CLI's own copy -- which drifted on blank handling and
// on wording.
type FieldTakenError struct {
	Field string
	Value string
}

func (e *FieldTakenError) Error() string {
	switch e.Field {
	case "username":
		return fmt.Sprintf("username %q is already taken", e.Value)
	case "email":
		return fmt.Sprintf("email %q is already in use", e.Value)
	default:
		return fmt.Sprintf("%s %q is already in use", e.Field, e.Value)
	}
}

// AvailabilityConnectError maps an availability failure onto a connect code.
//
// A collision is AlreadyExists; ANYTHING ELSE is Internal. Call sites used to
// wrap CheckEmailAvailable's result in AlreadyExists unconditionally, which
// reported a transient store failure ("check email: ...") to the client as a
// taken address -- a retryable fault rendered as a permanent one.
func AvailabilityConnectError(err error) error {
	if err == nil {
		return nil
	}
	var taken *FieldTakenError
	if errors.As(err, &taken) {
		return connect.NewError(connect.CodeAlreadyExists, taken)
	}
	// A refused conditional mint is the resend cooldown, not a fault: the
	// caller asked again too soon and the live code is still valid.
	if errors.Is(err, ErrVerificationCooldown) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// verifyPendingEmailToken validates the verification code submitted by
// the *session user* against their own pending row. Because the lookup
// is keyed by the session, no two users can ever collide on the short
// 6-character code — they each have at most one pending row.
//
// Logic:
//  1. Normalize the input (uppercase, strip hyphens/whitespace). A
//     malformed shape that can't be charset-checked is rejected as
//     InvalidArgument; the backend's own charset enforcement is the
//     source of truth.
//  2. Atomically charge one attempt and read back the post-update row.
//     The store helper bumps the counter, force-expires the row when
//     attempts > maxVerificationAttempts, and returns ErrNotFound when
//     there's no pending verification at all.
//  3. Expiry and mismatch collapse into a single NotFound with the
//     same message — the caller cannot tell which condition failed, so
//     we don't leak a timing/oracle signal.
//  4. On match, promote the pending email and return the fresh row.
func verifyPendingEmailToken(ctx context.Context, st store.Store, userID, submitted string) (*store.User, error) {
	normalized := verifycode.Normalize(submitted)
	if normalized == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid verification code"))
	}

	user, err := st.Users().ConsumeVerificationAttempt(ctx, userID, time.Now().UTC(), maxVerificationAttempts)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The store's WHERE filter only checks pending_email_token; an
	// empty pending_email with a non-empty token shouldn't happen via
	// the normal SetPendingEmail path but defending here makes the
	// promotion below safe.
	if user.PendingEmail == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no pending email change"))
	}

	if int(user.PendingEmailAttempts) > maxVerificationAttempts {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many attempts; request a new code"))
	}

	expired := user.PendingEmailExpiresAt == nil || !time.Now().UTC().Before(*user.PendingEmailExpiresAt)
	mismatch := subtle.ConstantTimeCompare([]byte(user.PendingEmailToken), []byte(normalized)) != 1
	if expired || mismatch {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("invalid or expired verification code"))
	}

	if err := promotePendingEmail(ctx, st, user.ID, user.PendingEmail); err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}

	updatedUser, err := st.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return updatedUser, nil
}

// promotePendingEmail moves pending_email to email with email_verified=true.
// It checks that no other user has claimed the verified email, then clears
// any other users' pending_email with the same value so they don't attempt
// to verify a now-taken address.
func promotePendingEmail(ctx context.Context, st store.Store, userID, email string) error {
	if err := CheckEmailAvailable(ctx, st, email, userID); err != nil {
		return fmt.Errorf("email was claimed by another user: %w", err)
	}
	if err := st.Users().PromotePendingEmail(ctx, userID); err != nil {
		return fmt.Errorf("promote pending email: %w", err)
	}
	ClearCompetingPendingEmails(ctx, st, email, userID)
	return nil
}

// ClearCompetingPendingEmails clears pending_email from all other users who
// have the same value. Call this whenever an email address is claimed — either
// by promotion or by direct assignment to the email column.
func ClearCompetingPendingEmails(ctx context.Context, st store.Store, email, ownerUserID string) {
	if email == "" {
		return
	}
	_ = st.Users().ClearCompetingPendingEmails(ctx, store.ClearCompetingPendingEmailsParams{
		PendingEmail: email,
		ExcludeID:    ownerUserID,
	})
}

// issuePendingEmailVerification stores a fresh pending_email row and
// dispatches the verification mail. The token is a 6-character
// verifycode (stored raw, displayed hyphenated); the mail body carries
// both the code and a click-through link to /verify-email, which is
// itself gated by login, so a leaked link alone cannot complete
// verification.
//
// Returns (sent, err): err signals a check or storage failure; sent=false
// means the mail send failed. On a failed send the undelivered CODE is
// dropped and the failure is logged server-side: a row whose code was
// never delivered would block the immediate retry behind the resend
// cooldown and leave the operator without a signal. The pending ADDRESS
// survives, because it is the only record of what the account is trying to
// verify -- a sign-up that requires verification leaves users.email empty,
// so wiping the whole row leaves ResendVerificationEmail with nothing to
// re-send to and the account permanently unverifiable. Email-change
// callers that surface the failure to the user inline can use
// issuePendingEmailVerificationOrFail instead.
func issuePendingEmailVerification(ctx context.Context, st store.Store, sender mail.Sender, renderer mail.Renderer, userID, email string, now time.Time) (bool, error) {
	storedCode, err := mintPendingEmailVerification(ctx, st, userID, email, now)
	if err != nil {
		return false, err
	}
	return deliverPendingEmailVerification(ctx, st, sender, renderer, userID, email, storedCode), nil
}

// mintPendingEmailVerification performs the two STORE steps and nothing
// else, and returns the code it minted.
//
// Split from the send so a caller that needs the mint INSIDE a transaction
// can take one. RequestEmailChange does: it moves the account's recovery
// address, so it must re-read the acting authority under the user-auth lock
// -- and an SMTP exchange must never hold that lock, which on SQLite is the
// single database writer (the same trade auth.Login makes for Argon2).
//
// ErrVerificationCooldown stays HERE, on the mint, because the conditional
// write is what carries it. That refusal is the one thing that stops an
// email-change RPC from being an open relay: the address is caller-supplied,
// so an unconditional mint sends a message per request to any address the
// caller names.
func mintPendingEmailVerification(ctx context.Context, st store.Store, userID, email string, now time.Time) (string, error) {
	if err := CheckEmailAvailable(ctx, st, email, userID); err != nil {
		return "", err
	}
	storedCode := verifycode.Generate()
	expiresAt := now.Add(pendingEmailExpiry).UTC()
	minted, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userID,
		PendingEmail:          email,
		PendingEmailToken:     storedCode,
		PendingEmailExpiresAt: &expiresAt,
		CooldownCutoff:        mintCutoff(now, pendingEmailExpiry),
	})
	if err != nil {
		return "", fmt.Errorf("set pending email: %w", err)
	}
	if !minted {
		return "", ErrVerificationCooldown
	}
	return storedCode, nil
}

// deliverPendingEmailVerification sends a minted code and reports whether the
// relay took it. On a failed send it drops the undelivered CODE and KEEPS the
// pending address; see clearUndeliveredVerificationCode.
//
// st is the plain store, never a transaction: this runs AFTER the mint
// commits, so the repair is its own write on the failure path rather than a
// rollback of a mint the caller may want to keep.
func deliverPendingEmailVerification(ctx context.Context, st store.Store, sender mail.Sender, renderer mail.Renderer, userID, email, storedCode string) bool {
	if err := sender.Send(ctx, renderer.VerificationEmail(email, storedCode, pendingEmailExpiry)); err != nil {
		if clearErr := clearUndeliveredVerificationCode(ctx, st, userID); clearErr != nil {
			slog.WarnContext(ctx, "clear undelivered verification code after failed send",
				"user_id", userID, "err", clearErr)
		}
		slog.WarnContext(ctx, "verification email send failed; dropped the code and kept the pending address for retry",
			"user_id", userID, "email", email, "err", err)
		return false
	}
	return true
}

// ErrVerificationCooldown reports a conditional mint that the resend
// cooldown refused. It is a sentinel, not a message match: the RPC layer
// maps it to ResourceExhausted, and infrastructure failures stay Internal.
var ErrVerificationCooldown = errors.New("a verification code was sent recently; wait before requesting another")

// clearUndeliveredVerificationCode drops a code that the relay refused, and
// KEEPS the address it was for.
//
// An empty token is the "no live code, address still pending" state: the
// verify path refuses it (ConsumeVerificationAttempt filters
// pending_email_token != ”), and the resend cooldown -- which reads the
// expiry -- does not apply, so the retry the failure message invites is
// never blocked. Clearing the address instead would strand a
// verification-required sign-up, whose users.email column is empty, with
// nothing to re-send to.
//
// The address is still subject to retention: ClearStalePendingEmails has a
// second arm that reaps a codeless row on updated_at, because the expiry
// compare cannot see one.
func clearUndeliveredVerificationCode(ctx context.Context, st store.Store, userID string) error {
	return st.Users().ClearPendingEmailCode(ctx, userID)
}
