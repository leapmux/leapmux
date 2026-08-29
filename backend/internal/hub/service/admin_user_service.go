package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminUserService implements the leapmux.v1.AdminUserService ConnectRPC
// handler: thin adapters over the same store operations the offline admin
// verbs used, behind the authenticated admin check. The offline
// break-glass variants (first-admin bootstrap, password reset with the
// hub stopped) stay in `leapmux recover`.
type AdminUserService struct {
	store     store.Store
	set       *settings.Manager
	validator *auth.TokenValidator
	// lifecycle tears down the in-process holders of a credential this
	// service revokes. The durable revocation events reach every hub, but
	// only on the watcher's next sweep, so without these effects an
	// administrator's revoke leaves the caller's own hub serving the
	// revoked credential for that whole interval. Every method is nil-safe.
	lifecycle *auth.CredentialLifecycleEffects
	// workerEffects runs the out-of-database half of a deregistration for
	// the workers a user deletion tears down.
	workerEffects *WorkerDeregisterEffects
	// mail and renderer send the issuance notice for the credential
	// IssueAPIToken mints. See notifyCredentialIssued.
	mail     mail.Sender
	renderer mail.Renderer

	// The clock this service reads. It mints an API token pair exactly as
	// OAuthServerHandler.issueAPIToken does, and it requires the same elevation
	// window for that mint, so the two must answer one instant or a test that
	// moves the clock moves half the surface.
	clockSeam
}

// AdminUserServiceDeps wires the service. It is a struct rather than a
// positional list because the list reached six once this verb grew a mailer,
// and six adjacent pointers at a call site are six a caller can transpose.
type AdminUserServiceDeps struct {
	Store     store.Store
	Settings  *settings.Manager
	Validator *auth.TokenValidator
	// Lifecycle tears down the in-process holders of a credential this
	// service revokes. Every method is nil-safe.
	Lifecycle *auth.CredentialLifecycleEffects
	// WorkerEffects runs the out-of-database half of a deregistration.
	WorkerEffects *WorkerDeregisterEffects
	// Mail and Renderer send the "a CLI credential was issued" notice, on the
	// same terms OAuthServerDeps states: Mail may be nil and issuance then
	// sends nothing, and Renderer is a struct value whose zero value is valid.
	Mail     mail.Sender
	Renderer mail.Renderer
}

func NewAdminUserService(deps AdminUserServiceDeps) *AdminUserService {
	return &AdminUserService{
		set:           deps.Settings,
		store:         deps.Store,
		validator:     deps.Validator,
		lifecycle:     deps.Lifecycle,
		workerEffects: deps.WorkerEffects,
		mail:          deps.Mail,
		renderer:      deps.Renderer,
	}
}

// adminUserToProto maps a user row. Password material never crosses.
func adminUserToProto(u *store.User) *leapmuxv1.AdminUser {
	out := &leapmuxv1.AdminUser{
		Id:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		PendingEmail:  u.PendingEmail,
		PasswordSet:   u.PasswordSet,
		IsAdmin:       u.IsAdmin,
		CreatedAt:     timestamppb.New(u.CreatedAt),
		UpdatedAt:     timestamppb.New(u.UpdatedAt),
	}
	return out
}

// resolveAdminUser resolves an (id | username) selector to a live user
// row, with the CLI verbs' refusal wording.
func resolveAdminUser(ctx context.Context, st store.Store, id, username string) (*store.User, error) {
	user, err := ResolveUserSelector(ctx, st, id, username)
	switch {
	case errors.Is(err, ErrNoUserSelector):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id or username is required"))
	case errors.Is(err, ErrAmbiguousUserSelector):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil && id != "":
		return nil, storeConnectError(err, "get user by id")
	case err != nil:
		return nil, storeConnectError(err, "get user by username")
	}
	return user, nil
}

// resolveAdminUserFilter resolves an optional (user_id | username)
// listing filter to a user-id pointer. The id path resolves through
// GetByIDIncludeDeleted: admin listings deliberately surface
// soft-deleted owners' still-live rows for audit. A username resolves
// among live users only — usernames are freed on soft-delete, so a name
// is not a stable handle for a deleted account.
func resolveAdminUserFilter(ctx context.Context, st store.Store, uid, username string) (*string, error) {
	if uid == "" && username == "" {
		return nil, nil
	}
	if uid != "" && username != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id and username are mutually exclusive"))
	}
	if uid != "" {
		user, err := st.Users().GetByIDIncludeDeleted(ctx, uid)
		if err != nil {
			return nil, storeConnectError(err, "get user by id")
		}
		return &user.ID, nil
	}
	user, err := st.Users().GetByUsername(ctx, username)
	if err != nil {
		return nil, storeConnectError(err, "get user by username")
	}
	return &user.ID, nil
}

// storeConnectError maps a store error onto the Connect surface: not
// found is the caller's selector, conflict is the caller's field, and
// anything else is internal.
func storeConnectError(err error, op string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: not found", op))
	case errors.Is(err, store.ErrConflict):
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s: conflict", op))
	case errors.Is(err, store.ErrInvalidCursor):
		// A stale or truncated cursor is the caller's to fix, and the fix
		// is not obvious from the parse error alone. ErrInvalidCursor is
		// its own sentinel and does NOT wrap ErrInvalidArgument, so
		// without this case it read as a hub fault — and WorkerManagement
		// classifies the same sentinel as InvalidArgument, so the two
		// admin surfaces disagreed.
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s: %w (pass the cursor printed at the end of the previous page, or omit it to start from the first page)", op, err))
	case errors.Is(err, store.ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// userConflictError renders a unique-index collision on a user write as a
// message that blames the field the caller actually supplied.
//
// The dialect layer cannot say WHICH index fired: every backend reports a
// duplicate as one opaque code, and the three dialects map all of them to
// store.ErrConflict. So the caller, which knows what it sent, identifies
// the field. Blaming both fields unconditionally is what produced
// `username "bob" or email "" is already taken` for a create with no
// email at all.
func userConflictError(err error, username, email string) error {
	if !errors.Is(err, store.ErrConflict) {
		return connect.NewError(connect.CodeInternal, err)
	}
	switch {
	case username != "" && email != "":
		return connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("username %q is already taken or email %q is already in use", username, email))
	case email != "":
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("email %q is already in use", email))
	case username != "":
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("username %q is already taken", username))
	default:
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("conflicting user record: %w", err))
	}
}

// revokeByID is the shape every single-credential revoke verb shares:
// refuse an empty id, run the store verb, and report a zero row count as
// NotFound with that verb's own wording. The store verbs all take one id
// and report the rows they changed, so the only per-verb parts are the
// operation label and the not-found message.
//
// The post-revoke lifecycle effect stays at the CALL SITE: it is
// per-credential-kind, and folding it in here would need the kind as a
// fourth parameter for no gain.
func revokeByID(ctx context.Context, id, op, notFoundFormat string, verb func(context.Context, string) (int64, error)) error {
	if err := requireField(id, "id"); err != nil {
		return err
	}
	n, err := verb(ctx, id)
	if err != nil {
		return storeConnectError(err, op)
	}
	if n == 0 {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf(notFoundFormat, id))
	}
	return nil
}

// revokeEveryUserCredential ends every credential one user holds: their
// sessions are deleted and each bearer they hold is revoked. It reports the
// two bearer counts and the auth generation THIS transaction committed.
//
// It runs inside the caller's RunInUserAuthTransaction, and it does only the
// durable half. The caller owns the other half and must, AFTER the commit,
// apply `s.lifecycle.UserRevoked(userID, committedGeneration)` --
// auth.RevokeAllUserCredentials requires an in-process caller to evict the
// auth-context registry itself, because the durable event reaches this hub
// only on the revocation watcher's next sweep, up to two seconds later. The
// effect stays at the call site because it cannot run from in here: a
// rollback after it would evict credentials that were never revoked. The
// generation comes back as a return value so no caller has to read it a
// second time and get whatever a concurrent revocation left.
//
// It takes the minted userid.UserID alone and spells the row id from it, so
// no caller can pass an id and a userid that identify two different users.
func revokeEveryUserCredential(ctx context.Context, tx store.Store, uid userid.UserID) (apiCount, delegationCount, committedGeneration int64, err error) {
	if err := tx.Sessions().DeleteByUser(ctx, uid); err != nil {
		return 0, 0, 0, fmt.Errorf("delete sessions: %w", err)
	}
	apiCount, delegationCount, err = auth.RevokeAllUserCredentials(ctx, tx, uid)
	if err != nil {
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

// The page-size limits every paginated admin RPC applies.
//
// Exported because the CLI reads them: it refuses an out-of-range --limit
// before the dial so the operator gets an answer that identifies the flag.
// The two sides therefore state ONE range. They act on it differently -- the
// hub caps an oversized limit and returns a page, the CLI refuses it -- and
// that is deliberate: an operator who asks for 900 rows is better served by
// the range than by 500 rows that look like the whole answer.
const (
	// DefaultPageLimit is what an omitted or non-positive limit
	// resolves to.
	DefaultPageLimit = 50
	// MaxPageLimit caps what one page may return, so a caller cannot
	// ask the hub for the whole table in one response.
	MaxPageLimit = 500
)

// MaxAPITokenTTLSeconds caps an issued API token's lifetime at one year.
//
// The ceiling is what makes the mint safe: the handler multiplies the
// requested seconds by time.Second, and an int64 with no ceiling WRAPS on
// that multiply -- ttl_seconds = 20000000000 wraps to roughly 18 days,
// passes the `ttl <= 0` guard, and mints a bearer that expires 634 years
// before the operator asked. A year is far past any real token lifetime,
// so the cap costs an operator nothing and removes the overflow entirely.
// It mirrors settings.MaxTimeoutSeconds and its message.
//
// DERIVED from auth.AbsoluteTokenLifetime rather than spelled again. The two
// held the same year by coincidence, each justified by its own reason -- the
// overflow guard here, the consent-outlives-nothing rule there -- so a change
// to either could leave a credential this verb mints living past the ceiling
// every other one respects.
const MaxAPITokenTTLSeconds = int64(auth.AbsoluteTokenLifetime / time.Second)

// NormalizePageParams normalizes one paginated request's cursor and limit.
//
// The normalization belongs HERE, not in a client. `store.ClampListLimit`
// preserves a limit of 0, and the paginated queries read 0 as "return no
// rows" — so a caller that simply omits `limit` (the proto3 default) gets
// an empty page it cannot tell apart from an empty table. Every paginated
// handler builds its PageParams through this function, so the guarantee
// holds for the CLI, the frontend, and any script alike.
//
// It is not admin-only, and its name no longer says it is: the worker
// listing and the account's own CLI-credential listing use it too, and a
// name that claims otherwise makes a reader check whether they may.
func NormalizePageParams(cursor string, limit int64) store.PageParams {
	switch {
	case limit <= 0:
		limit = DefaultPageLimit
	case limit > MaxPageLimit:
		limit = MaxPageLimit
	}
	return store.PageParams{Cursor: cursor, Limit: limit}
}

// requireField refuses an empty required request field, in one wording
// across every admin handler.
func requireField(value, name string) error {
	if value == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(name+" is required"))
	}
	return nil
}

// optTimestamp converts an optional time into its proto form, keeping nil
// as nil — which is the same wire outcome as leaving the field unset.
func optTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func adminSessionToProto(s store.ActiveSession) *leapmuxv1.AdminSession {
	return &leapmuxv1.AdminSession{
		Id:           s.ID,
		UserId:       s.UserID,
		Username:     s.Username,
		UserDeleted:  s.UserDeleted,
		CreatedAt:    timestamppb.New(s.CreatedAt),
		LastActiveAt: timestamppb.New(s.LastActiveAt),
		ExpiresAt:    timestamppb.New(s.ExpiresAt),
		IpAddress:    s.IPAddress,
		UserAgent:    s.UserAgent,
	}
}

func (s *AdminUserService) ListUsers(ctx context.Context, req *connect.Request[leapmuxv1.ListUsersRequest]) (*connect.Response[leapmuxv1.ListUsersResponse], error) {
	params := store.ListAllUsersParams{
		PageParams: NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	}
	var page store.Page[store.User]
	var err error
	if q := req.Msg.GetQuery(); q != "" {
		page, err = s.store.Users().Search(ctx, store.SearchUsersParams{
			Query:      &q,
			PageParams: params.PageParams,
		})
	} else {
		page, err = s.store.Users().ListAll(ctx, params)
	}
	if err != nil {
		return nil, storeConnectError(err, "list users")
	}
	users := make([]*leapmuxv1.AdminUser, 0, len(page.Rows))
	for _, u := range page.Rows {
		users = append(users, adminUserToProto(&u))
	}
	return connect.NewResponse(&leapmuxv1.ListUsersResponse{Users: users, NextCursor: page.NextCursor}), nil
}

func (s *AdminUserService) GetUser(ctx context.Context, req *connect.Request[leapmuxv1.GetUserRequest]) (*connect.Response[leapmuxv1.GetUserResponse], error) {
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.GetUserResponse{User: adminUserToProto(user)}), nil
}

// Creating an account is creating durable new authority: this verb takes a
// password the caller chooses and an is_admin flag, so a stolen credential
// that reaches it does not need to defeat any ceiling -- it makes a fresh
// administrator and signs in as them. See
// requireElevatedSessionForDurableAuthority.
func (s *AdminUserService) CreateUser(ctx context.Context, req *connect.Request[leapmuxv1.CreateUserRequest]) (*connect.Response[leapmuxv1.CreateUserResponse], error) {
	actor, err := requireElevatedSessionForDurableAuthority(ctx, s.now())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	username, err := validate.SanitizeSlug("username", msg.GetUsername())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if usernames.IsReservedSystem(username) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("username %q is reserved", username))
	}
	if err := validate.ValidatePassword(msg.GetPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	displayName, err := validate.SanitizeDisplayName(msg.GetDisplayName(), username)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
	}
	email := msg.GetEmail()
	if email != "" {
		if err := validate.ValidateEmail(email); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	// When the hub requires verification, an email-less unverified
	// non-admin account lands on /verify-email with no code that can ever
	// arrive: the login flow flags it verification-required but there is
	// nothing to verify. Administrators and explicit email_verified=true
	// requests pass.
	//
	// Through auth.EmailVerificationFacts.Satisfied, which is the ONE derivation
	// of the administrator exemption -- the interceptor, both login stages and
	// UpdateUser read it too. Spelled out here as a fourth copy, a later
	// change to the exemption would reach those three and silently miss
	// this one, and the two admin verbs would then disagree about which
	// accounts may exist without an address.
	//
	// The administrator branch rests on that exemption alone now. It used to
	// rest on CreateUser forcing email_verified in the database, which this
	// change removed: the column records whether somebody confirmed the
	// address, and forcing it made an administrator's unconfirmed address a
	// valid self-service password-reset target.
	if email == "" && s.set != nil && !(auth.EmailVerificationFacts{
		IsAdmin:       msg.GetIsAdmin(),
		EmailVerified: msg.GetEmailVerified(),
	}).Satisfied(settings.EmailVerificationEffective(s.set.Snapshot(ctx))) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("email is required when the hub requires email verification"))
	}

	// Pre-check availability so the hub reports a collision as a collision;
	// the unique index remains the real guard against the race.
	if err := CheckUsernameAvailable(ctx, s.store, username); err != nil {
		return nil, AvailabilityConnectError(err)
	}
	if err := CheckEmailAvailable(ctx, s.store, email, ""); err != nil {
		return nil, AvailabilityConnectError(err)
	}

	hashed, err := password.Hash(msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}
	// EmailVerified is what the operator asked for, and nothing forces it:
	// the column records whether somebody confirmed the address, and an
	// administrator's address is no more confirmed than anybody else's. The
	// login check takes its own exemption; see auth.EmailVerificationFacts.Satisfied.
	var user *store.User
	if err := commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		var createErr error
		user, createErr = CreateUser(ctx, s.store, CreateUserParams{
			Username:      username,
			PasswordHash:  hashed,
			DisplayName:   displayName,
			Email:         email,
			EmailVerified: msg.GetEmailVerified(),
			PasswordSet:   true,
			IsAdmin:       msg.GetIsAdmin(),
		})
		if createErr != nil {
			return userConflictError(createErr, username, email)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	created, err := s.store.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, storeConnectError(err, "get created user")
	}
	return connect.NewResponse(&leapmuxv1.CreateUserResponse{User: adminUserToProto(created)}), nil
}

// resolveEmailVerified decides what email_verified becomes after an admin
// UpdateUser, and whether the fenced verb must write it.
//
// A new address is unconfirmed, so an address change LOWERS email_verified
// unless the same request raises it explicitly. Carrying the old flag onto
// the new address marked an address nobody confirmed as verified -- and a
// verified address is a valid self-service password-reset target, so the
// carry handed the account's recovery route to whatever address the request
// carried.
//
// There is NO administrator exception, and there used to be. The column now
// records only whether somebody confirmed the address, so an administrator's
// new address is exactly as unconfirmed as anybody else's; the exemption
// that kept an administrator signed in lives at the login check instead (see
// auth.EmailVerificationFacts.Satisfied). With the carve-out, an administrator
// moved to a brand-new address kept a live self-service reset route to it.
//
// The rule applies to a NEW address only, and only when it differs from the
// one already on the account.
//
// Clearing the address is excluded on purpose. It leaves nothing for anybody
// to confirm, and lowering the flag there would mint the exact state the
// guard in UpdateUser refuses: an email-less unverified account on a hub
// that requires verification, which lands on /verify-email with no code that
// can ever arrive.
//
// Rewriting the same address is excluded because the confirmation that
// address already carries still holds. This rule compares both sides through
// NormalizeEmail, the same folding the write path applies, so a change of
// letter case alone does not read as a new address.
//
// write is true whenever the value CHANGES, or the request stated it. Every
// lowering must run through UpdateEmailVerified, the fenced verb, even when
// the request changed only the address: it bumps auth_generation and tears
// the user's leases and channels down, and UpdateEmail does not. Writing a
// lowered flag through UpdateEmail alone would leave every token of a
// now-unverified account live.
func resolveEmailVerified(user *store.User, msg *leapmuxv1.UpdateUserRequest) (value, write bool) {
	movedToANewAddress := msg.Email != nil && msg.GetEmail() != "" &&
		store.NormalizeEmail(msg.GetEmail()) != store.NormalizeEmail(user.Email)

	value = user.EmailVerified
	if movedToANewAddress {
		value = false
	}
	if msg.EmailVerified != nil {
		value = msg.GetEmailVerified()
	}
	return value, msg.EmailVerified != nil || value != user.EmailVerified
}

// An account's email address is a RECOVERY IDENTITY, so writing one is
// creating durable new authority.
//
// {email: attacker@example.com, email_verified: true} in one call moves where
// the public RequestPasswordReset mails a reset link, and that verb refuses
// only an UNVERIFIED address -- so this pair hands over any account, exactly
// as ResetPassword does, and leaves the victim's password working until the
// attacker chooses to reset it. Restricting ResetPassword and not this
// gained nothing: an un-elevated administrator cookie reached the same
// outcome by the longer route.
//
// TWO checks, because the verb writes two classes. The email fields take the
// strict session rule its sibling ResetPassword takes; a display-name edit or
// a cleared pending address only needs the acting credential's own proven
// factor, and refusing a bearer for those would break
// `leapmux control admin user update --display-name` for no gain.
func (s *AdminUserService) UpdateUser(ctx context.Context, req *connect.Request[leapmuxv1.UpdateUserRequest]) (*connect.Response[leapmuxv1.UpdateUserResponse], error) {
	msg := req.Msg
	user, err := resolveAdminUser(ctx, s.store, msg.GetId(), msg.GetUsername())
	if err != nil {
		return nil, err
	}
	updateDisplayName := msg.DisplayName != nil
	updateEmail := msg.Email != nil
	updateEmailVerified := msg.EmailVerified != nil
	clearPending := msg.GetClearPendingEmail()
	if !updateDisplayName && !updateEmail && !updateEmailVerified && !clearPending {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no fields to update"))
	}
	var actor *auth.UserInfo
	if updateEmail || updateEmailVerified {
		actor, err = requireElevatedSessionForDurableAuthority(ctx, s.now())
	} else {
		actor, err = requireElevatedActor(ctx, s.now())
	}
	if err != nil {
		return nil, err
	}

	var sanitizedDisplayName string
	if updateDisplayName {
		sanitizedDisplayName, err = validate.SanitizeDisplayName(msg.GetDisplayName(), user.Username)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display name: %w", err))
		}
	}
	if updateEmail && msg.GetEmail() != "" {
		if err := validate.ValidateEmail(msg.GetEmail()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	emailVerifiedAfter, applyEmailVerified := resolveEmailVerified(user, msg)

	// Clearing the email must not strand an account that cannot sign in: an
	// email-less unverified account on a hub that requires verification lands
	// on /verify-email with no code that can ever arrive. An administrator is
	// exempt because the LOGIN check exempts them -- the same derivation, at
	// the same altitude, rather than a forced column.
	//
	// The guard reads the flag this request RESOLVES, not the one already
	// stored. A request that clears the address and lowers the flag together
	// passed a guard that read the stored value, and then committed the exact
	// state the guard refuses -- the two-call form of the same edit
	// ({email_verified:false}, then {email:""}) was correctly refused, and the
	// one-call form was not.
	if updateEmail && msg.GetEmail() == "" && s.set != nil && !(auth.EmailVerificationFacts{
		IsAdmin:       user.IsAdmin,
		EmailVerified: emailVerifiedAfter,
	}).Satisfied(settings.EmailVerificationEffective(s.set.Snapshot(ctx))) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("cannot clear the email of an unverified account while the hub requires email verification"))
	}

	err = commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		return s.store.RunInTransaction(ctx, func(tx store.Store) error {
			if updateDisplayName {
				if err := tx.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
					Username:    user.Username,
					DisplayName: sanitizedDisplayName,
					ID:          user.ID,
				}); err != nil {
					return fmt.Errorf("update display name: %w", err)
				}
			}
			if updateEmail {
				if err := CheckEmailAvailable(ctx, tx, msg.GetEmail(), user.ID); err != nil {
					return AvailabilityConnectError(err)
				}
				// The address only, and the flag at its CURRENT value. Any change
				// to email_verified travels through its own verb below, whatever
				// this request also changes -- including the lowering that an
				// address change implies. Writing the lowered flag here instead
				// would leave the fenced verb nothing to reduce, so it would skip
				// the fence and leave every token of a now-unverified account
				// live: the exact bug the two separate blocks exist to prevent.
				// resolveEmailVerified decides the value; this call never does.
				if err := SetEmailAndClearCompeting(ctx, tx, user.ID, msg.GetEmail(), user.EmailVerified); err != nil {
					return userConflictError(err, "", msg.GetEmail())
				}
			}
			// A SEPARATE block, never the else-branch of the address update.
			// UpdateEmailVerified is the FENCED verb: lowering email_verified
			// weakens the user's authentication, so it must bump auth_generation
			// and
			// tear the user's leases and channels down. UpdateEmail is not
			// fenced. As one else-if, `{email, email_verified:false}` took the
			// unfenced path and left every token of a now-unverified account
			// live, while `{email_verified:false}` alone fenced them.
			if applyEmailVerified {
				if err := tx.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
					EmailVerified: emailVerifiedAfter,
					ID:            user.ID,
				}); err != nil {
					return fmt.Errorf("update email verified: %w", err)
				}
			}
			if clearPending {
				if err := tx.Users().ClearPendingEmail(ctx, user.ID); err != nil {
					return fmt.Errorf("clear pending email: %w", err)
				}
			}
			return nil
		})
	})
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	updated, err := s.store.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, storeConnectError(err, "get updated user")
	}
	return connect.NewResponse(&leapmuxv1.UpdateUserResponse{User: adminUserToProto(updated)}), nil
}

// Deleting an account is irreversible, so it takes the elevation window.
//
// The class the window guards is an irreversible move of a credential or an
// identity, and destruction is one: this verb soft-deletes the account, every
// workspace it owns and every worker it registered, revokes every credential
// it holds, and clears its passkeys -- with force, on another administrator.
// requireElevatedActor rather than the stricter session rule, because
// deletion creates no NEW way into an account and `leapmux control admin user
// delete` is a documented headless verb; the admin scope that admits a
// credential here was itself granted at a browser consent that demanded a
// proven factor.
func (s *AdminUserService) DeleteUser(ctx context.Context, req *connect.Request[leapmuxv1.DeleteUserRequest]) (*connect.Response[leapmuxv1.DeleteUserResponse], error) {
	actor, err := requireElevatedActor(ctx, s.now())
	if err != nil {
		return nil, err
	}
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	if user.IsAdmin && !req.Msg.GetForce() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("user %q is an admin; pass force to confirm deletion", user.Username))
	}
	delUID, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	// Collect the live worker ids BEFORE the transaction: after
	// MarkAllDeletedByUser the rows are soft-deleted, and the post-commit
	// effects still have to tell each of those workers to stop.
	workerIDs, err := s.liveWorkerIDs(ctx, delUID)
	if err != nil {
		return nil, err
	}
	var committedGeneration int64
	err = commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		return s.store.RunInUserAuthTransaction(ctx, delUID, func(tx store.Store) error {
			if err := tx.Workers().MarkAllDeletedByUser(ctx, delUID); err != nil {
				return fmt.Errorf("mark workers deleted: %w", err)
			}
			if err := tx.Workspaces().SoftDeleteAllByUser(ctx, delUID); err != nil {
				return fmt.Errorf("soft-delete workspaces: %w", err)
			}
			// User deletion implies every credential the user had dies with
			// it. The teardown must run BEFORE the soft-delete below:
			// LockUserAuthState filters `deleted_at IS NULL`, so a revoke
			// that ran after tx.Users().Delete would abort the transaction
			// and lose the cross-process teardown. store.RevokeUserTokens
			// states the rule.
			var err error
			if _, _, committedGeneration, err = revokeEveryUserCredential(ctx, tx, delUID); err != nil {
				return err
			}
			// Soft-delete does not CASCADE; clear passkey state now so
			// credential_id uniqueness and encrypted material do not linger.
			if err := RevokePasskeyAuthState(ctx, tx, user.ID); err != nil {
				return err
			}
			if err := tx.Users().Delete(ctx, user.ID); err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// AFTER the commit, never inside it. SendDeregister persists a
	// notification row and moves worker-manager state, so a rollback after
	// it would leave a worker told it was deregistered when it was not.
	s.lifecycle.UserRevoked(user.ID, committedGeneration)
	for _, workerID := range workerIDs {
		if err := s.workerEffects.Apply(ctx, workerID, user.ID); err != nil {
			// The user IS deleted and its workers ARE soft-deleted. A worker
			// that missed its stop notification reconciles on its next
			// connect, so this is worth a log, not a failed response that
			// invites a retry of an already-committed deletion.
			slog.Warn("user deleted but one worker was not told to stop",
				"user_id", user.ID, "worker_id", workerID, "error", err)
		}
	}
	return connect.NewResponse(&leapmuxv1.DeleteUserResponse{}), nil
}

// issuedByAnotherPerson reports whether somebody OTHER than the account owner
// issued this credential. It is the byAdministrator flag the issuance notice
// takes, and the notice's wording turns on it: "An administrator issued a
// command-line credential for your account. You did not authorize this
// yourself" against a plain receipt.
//
// resolveAdminUser accepts the actor's OWN id or username, so an
// administrator can issue a credential for themselves through this verb --
// and used to mail themselves that alarm. It trains the one reader who can
// act on a real one to treat the strongest notice the hub sends as noise.
//
// It FAILS CLOSED on a blank id, in the direction that keeps the alarm:
// userid.UserID.Matches reports false when either side is empty, so an actor
// the hub could not identify still reads as a third party.
func issuedByAnotherPerson(actor *auth.UserInfo, owner *store.User) bool {
	if actor == nil || owner == nil {
		return true
	}
	return !actor.ID.Matches(owner.ID)
}

// liveWorkerIDs pages every worker the user still owns. The admin
// deletion cascade needs the ids before it soft-deletes the rows.
func (s *AdminUserService) liveWorkerIDs(ctx context.Context, owner userid.UserID) ([]string, error) {
	var out []string
	cursor := ""
	for {
		page, err := s.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: owner,
			PageParams:   NormalizePageParams(cursor, MaxPageLimit),
		})
		if err != nil {
			return nil, storeConnectError(err, "list workers")
		}
		for _, w := range page.Rows {
			out = append(out, w.ID)
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// Granting administration is creating durable new authority. See
// requireElevatedSessionForDurableAuthority.
func (s *AdminUserService) SetUserAdmin(ctx context.Context, req *connect.Request[leapmuxv1.SetUserAdminRequest]) (*connect.Response[leapmuxv1.SetUserAdminResponse], error) {
	actor, err := requireElevatedSessionForDurableAuthority(ctx, s.now())
	if err != nil {
		return nil, err
	}
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	// Removing your OWN administrator access is irreversible from this
	// surface: UpdateAdmin runs the fenced verb, so it logs the caller out
	// at once, and the auth interceptor then denies every Admin* procedure
	// to the account. Recovery needs the offline
	// `leapmux recover bootstrap create-admin`, which itself refuses while
	// any administrator remains. So the caller must state the intent.
	if user.IsAdmin && !req.Msg.GetIsAdmin() {
		if actor.ID.String() == user.ID && !req.Msg.GetForce() {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("this removes your own administrator access; pass force to confirm"))
		}
	}
	// ONE write, because email_verified is no longer this verb's business.
	//
	// Promotion used to force the flag true and demotion had to repair what
	// the force left behind -- with a KNOWN GAP it could not close, because
	// the row recorded no reason for the flag and so could not tell a
	// confirmation from an invariant. The column now records only whether
	// somebody confirmed the address, which is a fact promotion does not
	// change, so there is nothing to force and nothing to repair. The
	// exemption that keeps an administrator signed in lives at the login
	// check; see auth.EmailVerificationFacts.Satisfied.
	if err := commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
				IsAdmin: req.Msg.GetIsAdmin(),
				ID:      user.ID,
			}); err != nil {
				return storeConnectError(err, "update admin flag")
			}
			return nil
		}); err != nil {
			var connectErr *connect.Error
			if errors.As(err, &connectErr) {
				return connectErr
			}
			return connect.NewError(connect.CodeInternal, err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	updated, err := s.store.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, storeConnectError(err, "get updated user")
	}
	return connect.NewResponse(&leapmuxv1.SetUserAdminResponse{User: adminUserToProto(updated)}), nil
}

// ResetPassword sets a new password for a user who cannot supply the old
// one, and tears down every credential the old password authenticated.
//
// The offline break-glass variant, for a hub that is stopped, is
// `leapmux recover password reset`. The two are not redundant and neither
// replaces the other: the offline verb opens the database directly, which a
// running hub must not do, and this one needs a running hub and an
// administrator login, which a hub that will not start cannot give. This one
// is also the only path that tears the credentials down IN PROCESS. The
// offline verb writes the same durable revocation events, but a running hub
// applies them only on its revocation watcher's next sweep.
//
// Resetting your OWN password ends your own sessions and bearer tokens as
// well, including the credential that made this call. That is deliberate:
// the effect must match the offline verb exactly, or the two paths drift.
// Unlike self-demotion on SetUserAdmin it is not irreversible -- the caller
// chose the new password and can log in again with it -- so it takes no
// force flag.
// Setting an account's password without the old one is creating durable new
// authority: it is account takeover by design, which is why it is an admin
// verb at all. See requireElevatedSessionForDurableAuthority.
func (s *AdminUserService) ResetPassword(ctx context.Context, req *connect.Request[leapmuxv1.ResetPasswordRequest]) (*connect.Response[leapmuxv1.ResetPasswordResponse], error) {
	actor, err := requireElevatedSessionForDurableAuthority(ctx, s.now())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	user, err := resolveAdminUser(ctx, s.store, msg.GetId(), msg.GetUsername())
	if err != nil {
		return nil, err
	}
	if err := validate.ValidatePassword(msg.GetPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hashed, err := password.Hash(msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hash password: %w", err))
	}
	uid, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	var apiCount, delegationCount, committedGeneration int64
	// commitUnderElevation re-reads the acting authority before the write. A
	// self-reset revokes the acting session inside the transaction, so the
	// slide that follows writes no rows -- which is the correct answer for a
	// credential the same call just took away.
	err = commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		return s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
			if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				PasswordHash: hashed,
				ID:           user.ID,
			}); err != nil {
				return fmt.Errorf("update password: %w", err)
			}
			// Admin password reset is break-glass: clear passkeys the same
			// way self-service CompletePasswordReset does, so a lost-device
			// recovery cannot leave orphan credentials, and a reset email
			// requested before this emergency reset must not stay
			// completable.
			if err := RevokePasskeyAuthState(ctx, tx, user.ID); err != nil {
				return err
			}
			// An administrator's reset rotates the user's auth basis
			// globally. Every credential that predates the rotation must
			// die, because whoever knew the old password is often the reason
			// for the reset.
			var err error
			apiCount, delegationCount, committedGeneration, err = revokeEveryUserCredential(ctx, tx, uid)
			return err
		})
	})
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// AFTER the commit, the half revokeEveryUserCredential leaves to its
	// caller.
	s.lifecycle.UserRevoked(user.ID, committedGeneration)
	return connect.NewResponse(&leapmuxv1.ResetPasswordResponse{
		UserId:                  user.ID,
		Username:                user.Username,
		ApiTokensRevoked:        apiCount,
		DelegationTokensRevoked: delegationCount,
	}), nil
}

func (s *AdminUserService) ListUserSessions(ctx context.Context, req *connect.Request[leapmuxv1.ListUserSessionsRequest]) (*connect.Response[leapmuxv1.ListUserSessionsResponse], error) {
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	uid, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	page, err := s.store.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
		UserID:     uid,
		PageParams: NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	}, s.now().UTC())
	if err != nil {
		return nil, storeConnectError(err, "list user sessions")
	}
	sessions := make([]*leapmuxv1.AdminSession, 0, len(page.Rows))
	for _, sess := range page.Rows {
		sessions = append(sessions, adminSessionToProto(store.ActiveSession{
			ID:           sess.ID,
			UserID:       sess.UserID,
			Username:     user.Username,
			CreatedAt:    sess.CreatedAt,
			LastActiveAt: sess.LastActiveAt,
			ExpiresAt:    sess.ExpiresAt,
			IPAddress:    sess.IPAddress,
			UserAgent:    sess.UserAgent,
		}))
	}
	return connect.NewResponse(&leapmuxv1.ListUserSessionsResponse{Sessions: sessions, NextCursor: page.NextCursor}), nil
}

func (s *AdminUserService) ListSessions(ctx context.Context, req *connect.Request[leapmuxv1.ListSessionsRequest]) (*connect.Response[leapmuxv1.ListSessionsResponse], error) {
	page, err := s.store.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
		PageParams: NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	}, s.now().UTC())
	if err != nil {
		return nil, storeConnectError(err, "list sessions")
	}
	sessions := make([]*leapmuxv1.AdminSession, 0, len(page.Rows))
	for _, sess := range page.Rows {
		sessions = append(sessions, adminSessionToProto(sess))
	}
	return connect.NewResponse(&leapmuxv1.ListSessionsResponse{Sessions: sessions, NextCursor: page.NextCursor}), nil
}

func (s *AdminUserService) RevokeSession(ctx context.Context, req *connect.Request[leapmuxv1.RevokeSessionRequest]) (*connect.Response[leapmuxv1.RevokeSessionResponse], error) {
	// Sessions().Revoke, not Delete: the two remove the same row, and the
	// event kind is what tells a step-up mutation waiting on the user-auth
	// lock that its acting session was TAKEN AWAY rather than signed out.
	if err := revokeByID(ctx, req.Msg.GetId(), "revoke session", "session not found: %s",
		s.store.Sessions().Revoke); err != nil {
		return nil, err
	}
	s.lifecycle.SessionRevoked(req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.RevokeSessionResponse{}), nil
}

func (s *AdminUserService) RevokeUserSessions(ctx context.Context, req *connect.Request[leapmuxv1.RevokeUserSessionsRequest]) (*connect.Response[leapmuxv1.RevokeUserSessionsResponse], error) {
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	uid, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	var apiCount, delegationCount, committedGeneration int64
	err = s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		var err error
		apiCount, delegationCount, committedGeneration, err = revokeEveryUserCredential(ctx, tx, uid)
		return err
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// AFTER the commit, the half revokeEveryUserCredential leaves to its
	// caller.
	s.lifecycle.UserRevoked(user.ID, committedGeneration)
	return connect.NewResponse(&leapmuxv1.RevokeUserSessionsResponse{
		ApiTokensRevoked:        apiCount,
		DelegationTokensRevoked: delegationCount,
	}), nil
}

func (s *AdminUserService) PurgeExpiredSessions(ctx context.Context, _ *connect.Request[leapmuxv1.PurgeExpiredSessionsRequest]) (*connect.Response[leapmuxv1.PurgeExpiredSessionsResponse], error) {
	n, err := s.store.Cleanup().HardDeleteExpiredSessions(ctx, s.now().UTC())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("purge expired sessions: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.PurgeExpiredSessionsResponse{Purged: n}), nil
}

func (s *AdminUserService) ListAPITokens(ctx context.Context, req *connect.Request[leapmuxv1.ListAPITokensRequest]) (*connect.Response[leapmuxv1.ListAPITokensResponse], error) {
	userFilter, err := resolveAdminUserFilter(ctx, s.store, req.Msg.GetUserId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	page, err := s.store.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		UserID:         userFilter,
		ClientID:       req.Msg.GetClientId(),
		PageParams:     NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
		IncludeRevoked: req.Msg.GetIncludeRevoked(),
	})
	if err != nil {
		return nil, storeConnectError(err, "list api tokens")
	}
	tokens := make([]*leapmuxv1.AdminAPIToken, 0, len(page.Rows))
	for _, t := range page.Rows {
		tokens = append(tokens, apiTokenToProto(t))
	}
	return connect.NewResponse(&leapmuxv1.ListAPITokensResponse{Tokens: tokens, NextCursor: page.NextCursor}), nil
}

func apiTokenToProto(t store.APITokenWithOwner) *leapmuxv1.AdminAPIToken {
	// What the credential REACHES, matching the account's own listing and the
	// validation path: the consent intersected with the app's registered
	// ceiling. An audit that reported the stored column would name permissions
	// an app cannot use, which is the opposite of what an audit is for.
	//
	// An unreadable value renders as NO permissions, on the same terms: an
	// audit must not show a credential as though its grant were legible when
	// it is not, and validation already refuses such a row.
	granted, _ := reachableGrantOf(t.GrantedScopes, t.ClientScopes)
	return &leapmuxv1.AdminAPIToken{
		Id:               t.ID,
		UserId:           t.UserID,
		Username:         t.OwnerUsername,
		OwnerDeleted:     t.OwnerDeleted,
		ClientId:         t.ClientID,
		ClientName:       t.ClientName,
		InstallationName: t.InstallationName,
		CreatedAt:        timestamppb.New(t.CreatedAt),
		LastUsedAt:       optTimestamp(t.LastUsedAt),
		ExpiresAt:        optTimestamp(t.ExpiresAt),
		RevokedAt:        optTimestamp(t.RevokedAt),
		GrantedScopes:    granted.SortedTokens(),
	}
}

func (s *AdminUserService) IssueAPIToken(ctx context.Context, req *connect.Request[leapmuxv1.IssueAPITokenRequest]) (*connect.Response[leapmuxv1.IssueAPITokenResponse], error) {
	msg := req.Msg
	// The SAME (id | username) selector every sibling verb takes. A third
	// spelling of "which user" is one an operator has to remember per verb.
	user, err := resolveAdminUser(ctx, s.store, msg.GetUserId(), msg.GetUsername())
	if err != nil {
		return nil, err
	}
	// The SAME cleaning the consent stages apply at intake. This verb writes the
	// same api_tokens.installation_name column, and the value reaches the
	// owner's connected-app list and the admin CLI's table -- so a newline here
	// writes arbitrary lines into both, and a long name fails on MySQL's
	// VARCHAR(255) while SQLite and Postgres take it. Cleaning BEFORE the empty
	// check also refuses a name of only control characters, which the raw check
	// accepted and stored as visible junk.
	installationName := normalizeInstallationName(msg.GetInstallationName())
	if installationName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("installation_name is required"))
	}
	// EMPTY identifies the built-in service-account registration, which runs no
	// flow. It exists so this out-of-band credential still identifies an app,
	// which
	// is what keeps api_tokens.client_id NOT NULL and every listing, join and
	// cascade free of a NULL branch.
	clientID := msg.GetClientId()
	if clientID == "" {
		clientID = oauthapp.ServiceAccountClientID
	}
	// The registration must exist and be live BEFORE the mint, for the same
	// reason every other stage resolves the app first: an unknown id used to die
	// on the foreign key inside Create and surface as a 500 for caller input,
	// and a retired app's id minted a dead-on-arrival credential that reported
	// itself as issued. Both are refusals here, where the operator can act on
	// them.
	app, err := s.store.OAuthClients().Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("no app is registered under client_id %s", clientID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load app: %w", err))
	}
	if app.RevokedAt != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this app is retired; issue the credential under a live registration instead"))
	}
	appCeiling, err := authscope.Parse(app.Scopes)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("app %s carries an unreadable scope ceiling: %w", clientID, err))
	}
	owner, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	if s.validator == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("token validator is not configured"))
	}
	actor, err := requireElevatedActor(ctx, s.now())
	if err != nil {
		return nil, err
	}

	secs := msg.GetTtlSeconds()
	if secs < 0 || secs > MaxAPITokenTTLSeconds {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("ttl_seconds must be between 0 and %d (got %d)", MaxAPITokenTTLSeconds, secs))
	}
	// The grant. EMPTY means "everything except the admin scopes", which
	// is what a headless service account needs and what the control CLI's own
	// default grant is -- so the two surfaces that mint a credential agree on
	// what "unspecified" means.
	granted, err := resolveIssuedScopes(msg.GetScopes(), user, actor)
	if err != nil {
		return nil, err
	}
	// The APP's registered ceiling is the third limit, and it narrows nothing
	// silently: the consent stages refuse an over-ceiling ask and so does the
	// refresh stage, so an administrator's mint refuses too. Narrowing here
	// instead would email and store a grant loadBearer strips at every
	// validation -- a credential that reports a width it does not have, which
	// is the exact failure the other refusals exist to prevent.
	if !appCeiling.Contains(granted) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("app %s is not registered for every permission this credential asks for", app.ClientName))
	}
	grantedString, err := granted.Storable()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// ttl_seconds picks WHICH KIND of credential this is, and the two kinds
	// are exclusive.
	//
	// Zero asks for the ordinary rotating one: an hour of access plus a
	// refresh stage, exactly what a consent stage mints, capped for its whole
	// life by auth.AbsoluteTokenLifetime.
	//
	// A positive value asks for a fixed-lifetime service credential, and it
	// gets NO refresh stage. Handing back both was the defect: the row records
	// an expiry and never the TTL it was minted from, so the first rotation
	// rewrote a year of access to one hour (auth.AccessWindowFor clips every
	// rotation to the ordinary window) and the configured lifetime was
	// unrecoverable. A credential that does not rotate cannot lose what it
	// was minted with.

	// The kind this call mints, from the rule above.
	rotating := secs <= 0
	// commitUnderElevation re-reads the acting authority before the mint --
	// the check above answered from a cached UserInfo, and what this writes
	// outlives the session by months -- and slides the window after it.
	var minted mintedAPIToken
	if err := commitUnderElevation(ctx, s.store, actor, s.now, func() error {
		var mintErr error
		minted, mintErr = mintAPIToken(s.validator, mintedByActor(actor), s.now(), apiTokenMint{
			UserID:           owner,
			ClientID:         clientID,
			InstallationName: installationName,
			GrantedScopes:    grantedString,
			AccessTTL:        time.Duration(secs) * time.Second,
			Rotating:         rotating,
		})
		if mintErr != nil {
			return mintErr
		}
		if err := s.store.APITokens().Create(ctx, minted.Params); err != nil {
			return storeConnectError(err, "create token")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// The owner learns about a credential minted on their account from the
	// admin surface, exactly as they do from a consent stage. This surface is
	// the one a stolen administrator cookie reaches, so it is the last one
	// that should mint in silence.
	noticeName := app.ClientName
	if noticeName == "" {
		noticeName = clientID
	}
	notifyCredentialIssued(ctx, s.mail, s.renderer, user, credentialNotice{
		AppName:          noticeName,
		InstallationName: installationName,
		Scopes:           granted.SortedTokens(),
		IssuedByAdmin:    issuedByAnotherPerson(actor, user),
	})
	// The secrets cross the wire exactly once; they cannot be retrieved. The
	// client id and the granted scopes state what the hub DECIDED, not what
	// was asked: the envelope is the one record an operator captures, and an
	// empty request takes the non-admin default -- reporting the request made
	// a ~14-permission credential read as `scopes: null`.
	return connect.NewResponse(&leapmuxv1.IssueAPITokenResponse{
		TokenId:       minted.TokenID,
		AccessToken:   minted.Pair.AccessBearer,
		RefreshToken:  minted.RefreshBearer(),
		ClientId:      clientID,
		GrantedScopes: granted.SortedTokens(),
	}), nil
}

func (s *AdminUserService) RevokeAPIToken(ctx context.Context, req *connect.Request[leapmuxv1.RevokeAPITokenRequest]) (*connect.Response[leapmuxv1.RevokeAPITokenResponse], error) {
	if err := revokeByID(ctx, req.Msg.GetId(), "revoke api token", "token %s not found or already revoked",
		s.store.APITokens().Revoke); err != nil {
		return nil, err
	}
	s.lifecycle.BearerRevoked(auth.BearerKindAPI, req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.RevokeAPITokenResponse{}), nil
}

func (s *AdminUserService) ListDelegationTokens(ctx context.Context, req *connect.Request[leapmuxv1.ListDelegationTokensRequest]) (*connect.Response[leapmuxv1.ListDelegationTokensResponse], error) {
	userFilter, err := resolveAdminUserFilter(ctx, s.store, req.Msg.GetUserId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	page, err := s.store.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
		UserID:         userFilter,
		PageParams:     NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
		IncludeRevoked: req.Msg.GetIncludeRevoked(),
	})
	if err != nil {
		return nil, storeConnectError(err, "list delegation tokens")
	}
	tokens := make([]*leapmuxv1.AdminDelegationToken, 0, len(page.Rows))
	for _, t := range page.Rows {
		tokens = append(tokens, delegationTokenToProto(t))
	}
	return connect.NewResponse(&leapmuxv1.ListDelegationTokensResponse{Tokens: tokens, NextCursor: page.NextCursor}), nil
}

func delegationTokenToProto(t store.DelegationTokenWithOwner) *leapmuxv1.AdminDelegationToken {
	return &leapmuxv1.AdminDelegationToken{
		Id:           t.ID,
		UserId:       t.UserID,
		Username:     t.OwnerUsername,
		OwnerDeleted: t.OwnerDeleted,
		WorkerId:     t.WorkerID,
		AgentId:      t.AgentID,
		CreatedAt:    timestamppb.New(t.CreatedAt),
		// A delegation token always expires; the other two are optional.
		ExpiresAt:  timestamppb.New(t.ExpiresAt),
		LastUsedAt: optTimestamp(t.LastUsedAt),
		RevokedAt:  optTimestamp(t.RevokedAt),
	}
}

func (s *AdminUserService) RevokeDelegationToken(ctx context.Context, req *connect.Request[leapmuxv1.AdminUserServiceRevokeDelegationTokenRequest]) (*connect.Response[leapmuxv1.AdminUserServiceRevokeDelegationTokenResponse], error) {
	if err := revokeByID(ctx, req.Msg.GetId(), "revoke delegation token",
		"delegation token %s not found or already revoked",
		s.store.DelegationTokens().Revoke); err != nil {
		return nil, err
	}
	s.lifecycle.BearerRevoked(auth.BearerKindDelegation, req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.AdminUserServiceRevokeDelegationTokenResponse{}), nil
}

// resolveIssuedScopes decides the grant an administrator's out-of-band
// credential carries.
//
// An EMPTY request means every scope EXCEPT the admin family. That is the
// same default the control CLI's own login takes, so the two surfaces that mint
// a credential agree on what "unspecified" means -- and it preserves the
// property the deleted admin_scope column defended: an ordinary credential can
// do everything its owner can do except administer the hub.
//
// An admin scope needs an administrator OWNER. Granting one to somebody else
// would mint a credential whose grant says one thing and whose every admin call
// is refused for another, which is the failure the refusal exists to prevent:
// the operator learns now rather than at the first admin verb.
func resolveIssuedScopes(tokens []string, owner *store.User, actor *auth.UserInfo) (authscope.ScopeSet, error) {
	requested := authscope.NonAdminGrant()
	if len(tokens) > 0 {
		parsed, err := authscope.Parse(strings.Join(tokens, " "))
		if err != nil {
			return authscope.ScopeSet{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		requested = parsed
	}
	if !owner.IsAdmin {
		if scope, found := firstAdminScope(requested); found {
			token, _ := authscope.Token(scope)
			return authscope.ScopeSet{}, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("user %s is not an administrator, so a credential for them cannot carry %s",
					owner.Username, token))
		}
	}
	// Closed BEFORE the ceiling check, not only at the return. Close() adds the
	// implied scopes (git:write carries git:read and worker:read), and the
	// stored set is the closed one, so checking the unclosed request against
	// the issuer and closing afterwards would let a mint gain every implied
	// scope past the check that was supposed to bound it -- an issuer whose
	// reachable set is unclosed, the hand-edited or restored row loadBearer
	// defends against, could mint a credential wider than anything it could
	// itself reach.
	requested = requested.Close()
	// The ISSUER's own grant is a ceiling, and it is the second bound this verb
	// needs. The scope rung admits an app with admin:users; without this, such
	// an app could mint ITSELF a credential carrying tunnel:open -- a total
	// bypass of the scope model rather than a wide grant.
	//
	// It REFUSES rather than silently narrows, for the reason every other scope
	// refusal here does: an operator told "issued" and then refused on the
	// first call has nothing to point at.
	if actor != nil && !actor.Scopes.Contains(requested) {
		return authscope.ScopeSet{}, connect.NewError(connect.CodePermissionDenied,
			errors.New("this credential cannot issue a credential wider than itself"))
	}
	// Closed here, at the mint, so the stored set is the one every check reads.
	return requested, nil
}
