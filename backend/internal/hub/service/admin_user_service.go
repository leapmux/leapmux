package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"log/slog"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminUserService implements the leapmux.v1.AdminUserService ConnectRPC
// handler: thin adapters over the same store operations the offline admin
// verbs used, behind the authenticated admin gate. The offline
// break-glass variants (first-admin bootstrap, password reset with the
// hub stopped) stay in `leapmux recover`.
type AdminUserService struct {
	store     store.Store
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
}

func NewAdminUserService(st store.Store, validator *auth.TokenValidator, lifecycle *auth.CredentialLifecycleEffects, workerEffects *WorkerDeregisterEffects) *AdminUserService {
	return &AdminUserService{store: st, validator: validator, lifecycle: lifecycle, workerEffects: workerEffects}
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
		// without this arm it read as a hub fault — and WorkerManagement
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
// no caller can pass an id and a userid that name two different users.
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

// Page-size bounds every paginated admin RPC applies.
//
// Exported because the CLI reads them: it refuses an out-of-range --limit
// before the dial so the operator gets an answer that identifies the flag.
// The two sides therefore name ONE range. They act on it differently -- the
// hub caps an oversized limit and returns a page, the CLI refuses it -- and
// that is deliberate: an operator who asks for 900 rows is better served by
// the range than by 500 rows that look like the whole answer.
const (
	// DefaultAdminPageLimit is what an omitted or non-positive limit
	// resolves to.
	DefaultAdminPageLimit = 50
	// MaxAdminPageLimit caps what one page may return, so a caller cannot
	// ask the hub for the whole table in one response.
	MaxAdminPageLimit = 500
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
const MaxAPITokenTTLSeconds = 365 * 24 * 60 * 60

// AdminPageParams normalizes one paginated request's cursor and limit.
//
// The normalization belongs HERE, not in a client. `store.ClampListLimit`
// preserves a limit of 0, and the paginated queries read 0 as "return no
// rows" — so a caller that simply omits `limit` (the proto3 default) gets
// an empty page it cannot tell apart from an empty table. Every paginated
// admin handler builds its PageParams through this function, so the
// guarantee holds for the CLI, the frontend, and any script alike.
func AdminPageParams(cursor string, limit int64) store.PageParams {
	switch {
	case limit <= 0:
		limit = DefaultAdminPageLimit
	case limit > MaxAdminPageLimit:
		limit = MaxAdminPageLimit
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
		PageParams: AdminPageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
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

func (s *AdminUserService) CreateUser(ctx context.Context, req *connect.Request[leapmuxv1.CreateUserRequest]) (*connect.Response[leapmuxv1.CreateUserResponse], error) {
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

	// Pre-check availability so a collision is reported as a collision;
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
	user, err := CreateUser(ctx, s.store, CreateUserParams{
		Username:      username,
		PasswordHash:  hashed,
		DisplayName:   displayName,
		Email:         email,
		EmailVerified: msg.GetEmailVerified(),
		PasswordSet:   true,
		IsAdmin:       msg.GetIsAdmin(),
	})
	if err != nil {
		return nil, userConflictError(err, username, email)
	}
	created, err := s.store.Users().GetByID(ctx, user.ID)
	if err != nil {
		return nil, storeConnectError(err, "get created user")
	}
	return connect.NewResponse(&leapmuxv1.CreateUserResponse{User: adminUserToProto(created)}), nil
}

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

	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
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
			// The address only. email_verified travels through its own verb
			// below, whatever this request also changes.
			if err := SetEmailAndClearCompeting(ctx, tx, user.ID, msg.GetEmail(), user.EmailVerified); err != nil {
				return userConflictError(err, "", msg.GetEmail())
			}
		}
		// A SEPARATE block, never the else-arm of the address update.
		// UpdateEmailVerified is the FENCED verb: lowering email_verified
		// reduces the user's auth gate, so it must bump auth_generation and
		// tear the user's leases and channels down. UpdateEmail is not
		// fenced. As one else-if, `{email, email_verified:false}` took the
		// unfenced path and left every token of a now-unverified account
		// live, while `{email_verified:false}` alone fenced them.
		if updateEmailVerified {
			if err := tx.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
				EmailVerified: msg.GetEmailVerified(),
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

func (s *AdminUserService) DeleteUser(ctx context.Context, req *connect.Request[leapmuxv1.DeleteUserRequest]) (*connect.Response[leapmuxv1.DeleteUserResponse], error) {
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
	err = s.store.RunInUserAuthTransaction(ctx, delUID, func(tx store.Store) error {
		if err := tx.Workers().MarkAllDeletedByUser(ctx, delUID); err != nil {
			return fmt.Errorf("mark workers deleted: %w", err)
		}
		if err := tx.Workspaces().SoftDeleteAllByUser(ctx, delUID); err != nil {
			return fmt.Errorf("soft-delete workspaces: %w", err)
		}
		// User deletion implies every credential the user had dies with it.
		// The teardown must run BEFORE the soft-delete below:
		// LockUserAuthState filters `deleted_at IS NULL`, so a revoke that
		// ran after tx.Users().Delete would abort the transaction and lose
		// the cross-process teardown. store.RevokeUserTokens states the rule.
		var err error
		if _, _, committedGeneration, err = revokeEveryUserCredential(ctx, tx, delUID); err != nil {
			return err
		}
		if err := tx.Users().Delete(ctx, user.ID); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		return nil
	})
	if err != nil {
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

// liveWorkerIDs pages every worker the user still owns. The admin
// deletion cascade needs the ids before it soft-deletes the rows.
func (s *AdminUserService) liveWorkerIDs(ctx context.Context, owner userid.UserID) ([]string, error) {
	var out []string
	cursor := ""
	for {
		page, err := s.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: owner,
			PageParams:   AdminPageParams(cursor, MaxAdminPageLimit),
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

func (s *AdminUserService) SetUserAdmin(ctx context.Context, req *connect.Request[leapmuxv1.SetUserAdminRequest]) (*connect.Response[leapmuxv1.SetUserAdminResponse], error) {
	user, err := resolveAdminUser(ctx, s.store, req.Msg.GetId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	// Removing your OWN administrator access is a one-way door from this
	// surface: UpdateAdmin runs the fenced verb, so it logs the caller out
	// at once, and the auth interceptor then denies every Admin* procedure
	// to the account. Recovery needs the offline
	// `leapmux recover bootstrap create-admin`, which itself refuses while
	// any administrator remains. So the caller must state the intent.
	if user.IsAdmin && !req.Msg.GetIsAdmin() {
		actor, err := auth.MustGetUser(ctx)
		if err != nil {
			return nil, err
		}
		if actor.ID.String() == user.ID && !req.Msg.GetForce() {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("this removes your own administrator access; pass force to confirm"))
		}
	}
	if err := s.store.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
		IsAdmin: req.Msg.GetIsAdmin(),
		ID:      user.ID,
	}); err != nil {
		return nil, storeConnectError(err, "update admin flag")
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
// Unlike self-demotion on SetUserAdmin it is not a one-way door -- the
// caller chose the new password and can log in again with it -- so it takes
// no force flag.
func (s *AdminUserService) ResetPassword(ctx context.Context, req *connect.Request[leapmuxv1.ResetPasswordRequest]) (*connect.Response[leapmuxv1.ResetPasswordResponse], error) {
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
	err = s.store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
			PasswordHash: hashed,
			ID:           user.ID,
		}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		// An administrator's reset rotates the user's auth basis globally.
		// Every credential that predates the rotation must die, because
		// whoever knew the old password is often the reason for the reset.
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
		PageParams: AdminPageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	})
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
		PageParams: AdminPageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	})
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
	if err := revokeByID(ctx, req.Msg.GetId(), "revoke session", "session not found: %s",
		s.store.Sessions().Delete); err != nil {
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
	n, err := s.store.Cleanup().HardDeleteExpiredSessions(ctx)
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
		ClientType:     req.Msg.GetClientType(),
		PageParams:     AdminPageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
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
	return &leapmuxv1.AdminAPIToken{
		Id:           t.ID,
		UserId:       t.UserID,
		Username:     t.OwnerUsername,
		OwnerDeleted: t.OwnerDeleted,
		ClientType:   t.ClientType,
		ClientName:   t.ClientName,
		CreatedAt:    timestamppb.New(t.CreatedAt),
		LastUsedAt:   optTimestamp(t.LastUsedAt),
		ExpiresAt:    optTimestamp(t.ExpiresAt),
		RevokedAt:    optTimestamp(t.RevokedAt),
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
	if msg.GetClientName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_name is required"))
	}
	owner, err := mintRowUserID(user.ID)
	if err != nil {
		return nil, err
	}
	if s.validator == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("token validator is not configured"))
	}

	secs := msg.GetTtlSeconds()
	if secs < 0 || secs > MaxAPITokenTTLSeconds {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("ttl_seconds must be between 0 and %d (got %d)", MaxAPITokenTTLSeconds, secs))
	}
	tokenID := id.Generate()
	now := time.Now()
	ttl := time.Duration(secs) * time.Second
	if ttl <= 0 {
		ttl = auth.AccessTokenTTL
	}
	pair := s.validator.MintBearerPair(auth.BearerKindAPI, tokenID, now, ttl, auth.RefreshTokenTTL)
	if err := s.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           owner,
		ClientType:       msg.GetClientType(),
		ClientName:       msg.GetClientName(),
		SecretHash:       pair.AccessHash,
		RefreshHash:      pair.RefreshHash,
		ExpiresAt:        &pair.AccessExpiresAt,
		RefreshExpiresAt: &pair.RefreshExpiresAt,
	}); err != nil {
		return nil, storeConnectError(err, "create token")
	}
	// The secrets cross the wire exactly once; they cannot be retrieved.
	return connect.NewResponse(&leapmuxv1.IssueAPITokenResponse{
		TokenId:      tokenID,
		AccessToken:  pair.AccessBearer,
		RefreshToken: pair.RefreshBearer,
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
		PageParams:     AdminPageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
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
