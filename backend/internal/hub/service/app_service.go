package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/util/validate"
)

// AppService is the app REGISTRATION surface: where an app comes from, as
// opposed to what one account authorized.
//
// Every verb answers a SESSION and nothing else -- procedure_scopes.go records
// the whole service as ScopeNever -- so this file never asks what an app was
// granted. What it does ask, on every write, is who owns the registration.
//
// Ownership travels INTO the statement rather than being checked here and
// applied there. store.UpdateOAuthClientParams carries CallerUserID and
// CallerIsAdmin, and each query filters on them, so a handler that forgot the
// guard writes nothing instead of writing somebody else's row. The audit
// tripwire in internal/audit/storebind.go is what keeps that arrangement.
type AppService struct {
	store    store.Store
	settings *settings.Manager
	// validator hashes a new client secret. It is the same hash the token
	// endpoint compares, which is why it is the validator rather than a local
	// helper.
	validator *auth.TokenValidator
	// lifecycle applies each revoked credential's effects after the cascade
	// commits. Effects ACCUMULATE, so they must not run inside a transaction
	// the store may retry.
	lifecycle *auth.CredentialLifecycleEffects
	now       func() time.Time
}

var _ leapmuxv1connect.AppServiceHandler = (*AppService)(nil)

// NewAppService wires the registration surface.
func NewAppService(
	st store.Store,
	setMgr *settings.Manager,
	validator *auth.TokenValidator,
	lifecycle *auth.CredentialLifecycleEffects,
) *AppService {
	return &AppService{
		store:     st,
		settings:  setMgr,
		validator: validator,
		lifecycle: lifecycle,
		now:       time.Now,
	}
}

// requireElevatedOwner is this service's elevation check, and its doc is the
// whole rule.
//
// FOUR verbs create or move authority and call it: RegisterApp, UpdateApp,
// SetAppElevationAllowed and VerifyApp. The one the rule was written for is
// UpdateApp -- rewriting a redirect list diverts an in-flight authorization
// code to an address the editor chose, which is the most dangerous write on
// this surface. RegisterApp mints a client secret and a row that writes the
// next consent screen, SetAppElevationAllowed hands an app the ceremony that
// multiplies every scope it holds, and VerifyApp removes the "unverified"
// label a person reads before granting.
//
// THREE verbs take none, each for a stated reason:
//
//   - ListApps reads the caller's own registrations.
//   - RevokeApp only REDUCES access, and it is the remedy somebody reaches
//     for on realizing an app is malicious. Demanding a fresh factor there is
//     the wrong failure mode -- the same argument the account's Connected apps
//     row makes for Disconnect.
//   - DeleteApp is refused while any credential row exists, so the reduction
//     already happened through RevokeApp; what is left is removing an empty
//     record.
//
// It calls requireElevatedActor rather than the session-only rule, because an
// app registration is a headless errand as much as a browser one -- `leapmux
// control admin app register` is a documented path, and a command-line
// credential proves its factor through /oauth/step-up.
//
// The classification record is appProcedureElevation in
// app_procedures_internal_test.go, and its tripwire fails the suite when
// app.proto grows a verb nobody decided about.
func (s *AppService) requireElevatedOwner(ctx context.Context) (*auth.UserInfo, error) {
	return requireElevatedActor(ctx, s.now())
}

// RegisterApp creates a registration.
//
// The client secret crosses ONCE, in the response. The hub stores its hash, so
// there is no later read and no rotation verb pretending otherwise.
//
// Elevated, like every write here. See requireElevatedOwner.
func (s *AppService) RegisterApp(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RegisterAppRequest],
) (*connect.Response[leapmuxv1.RegisterAppResponse], error) {
	user, err := s.requireElevatedOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg

	owner, source, err := s.resolveOwnership(msg.GetVisibility(), user)
	if err != nil {
		return nil, err
	}
	scopes, err := scopesFromProto(msg.GetScopes())
	if err != nil {
		return nil, err
	}
	if err := refuseAdminCeilingToNonAdmin(user, scopes); err != nil {
		return nil, err
	}
	params, secret, buildErr := s.buildAppParams(msg, owner, user.ID.String(), source, scopes)
	if buildErr != nil {
		return nil, buildErr
	}
	// The stored ROW, not a projection of the params: Create returns what the
	// database wrote, so the response's created_at/updated_at are the DB's own
	// values and no Go-side literal restates the column list. A brand-new
	// client cannot hold a credential yet (the foreign key has nothing to
	// point at), so the live count is zero and nobody has vouched for it.
	row, err := s.store.OAuthClients().Create(ctx, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create app: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.RegisterAppResponse{
		App:          appToProto(row, 0, ""),
		ClientSecret: secret,
	}), nil
}

// ListApps pages the registrations the caller may EDIT.
//
// That is a different question from what they may AUTHORIZE: the hub-wide
// catalogue is authorizable by everybody and editable by an administrator, so
// an ordinary user's list holds their own apps alone.
func (s *AppService) ListApps(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListAppsRequest],
) (*connect.Response[leapmuxv1.ListAppsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	params := store.ListOAuthClientsParams{
		UserID: user.ID,
		// Through NormalizePageParams, like every other paginated handler: a
		// limit of 0 reaches the queries as "return no rows", so an omitted
		// field -- the proto3 default -- would give an empty page nobody could
		// tell from an empty table.
		PageParams:     NormalizePageParams(req.Msg.GetCursor(), int64(req.Msg.GetLimit())),
		IncludeRevoked: req.Msg.GetIncludeRevoked(),
	}
	// An administrator edits the hub-wide catalogue AND their own private
	// apps; everybody else sees what they registered. One statement answers
	// every shape, on the IncludeHubWide and HubWideOnly flags. A request
	// that names one reach narrows it: the administration panel asks for
	// the catalogue alone, so an administrator's own private rows do not
	// cross the wire only to be discarded by a panel that cannot draw them.
	switch vis := req.Msg.GetVisibility(); vis {
	case leapmuxv1.AppVisibility_APP_VISIBILITY_UNSPECIFIED:
		params.IncludeHubWide = user.IsAdmin
	case leapmuxv1.AppVisibility_APP_VISIBILITY_PRIVATE:
		params.IncludeHubWide = false
	case leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE:
		if !user.IsAdmin {
			return nil, connect.NewError(connect.CodePermissionDenied,
				errors.New("only an administrator may list the hub-wide catalogue"))
		}
		params.HubWideOnly = true
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown app visibility: %s", vis))
	}
	page, err := s.store.OAuthClients().List(ctx, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list apps: %w", err))
	}

	// ONE round trip for the whole page's live counts, and one lookup per
	// DISTINCT vouching administrator: a listing that grows with the number of
	// registered apps must not grow a store call per row.
	live := map[string]int64{}
	if len(page.Rows) > 0 {
		ids := make([]string, 0, len(page.Rows))
		for i := range page.Rows {
			ids = append(ids, page.Rows[i].ClientID)
		}
		live, err = s.store.OAuthClients().CountLiveTokensByClients(ctx, ids)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count app credentials: %w", err))
		}
	}
	verifiers := map[string]string{}
	out := make([]*leapmuxv1.App, 0, len(page.Rows))
	for i := range page.Rows {
		verifier := ""
		if page.Rows[i].VerifiedAt != nil && page.Rows[i].VerifiedBy != "" {
			verifier = s.usernameOfMemoized(ctx, page.Rows[i].VerifiedBy, verifiers)
		}
		out = append(out, appToProto(&page.Rows[i], live[page.Rows[i].ClientID], verifier))
	}
	return connect.NewResponse(&leapmuxv1.ListAppsResponse{
		Apps:                    out,
		NextCursor:              page.NextCursor,
		OpenRegistrationEnabled: settings.KeyOpenAppRegistration.Of(s.snapshot(ctx)),
	}), nil
}

// UpdateApp rewrites the editable fields.
//
// Each field is optional and an absent one is left alone, so a caller that
// changes a name does not send back a redirect list it read minutes ago and
// overwrite a concurrent edit with it.
//
// Elevated, and this is the verb the rule was written for: rewriting a
// redirect list diverts an in-flight authorization code to an address the
// editor chose. See requireElevatedOwner.
func (s *AppService) UpdateApp(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateAppRequest],
) (*connect.Response[leapmuxv1.UpdateAppResponse], error) {
	user, err := s.requireElevatedOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	app, err := s.loadOwned(ctx, msg.GetClientId(), user)
	if err != nil {
		return nil, err
	}
	if err := refuseBuiltInEdit(app); err != nil {
		return nil, err
	}

	next := store.UpdateOAuthClientParams{
		ClientID:         app.ClientID,
		ClientName:       app.ClientName,
		ClientURI:        app.ClientURI,
		RedirectURIs:     app.RedirectURIs,
		Scopes:           app.Scopes,
		GrantTypes:       app.GrantTypes,
		ElevationAllowed: app.ElevationAllowed,
		CallerUserID:     user.ID,
		CallerIsAdmin:    user.IsAdmin,
	}
	if msg.ClientName != nil {
		name := validate.CleanNameTo(msg.GetClientName(), clientNameByteLimit)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("client_name cannot be empty"))
		}
		next.ClientName = name
	}
	if msg.ClientUri != nil {
		uri := strings.TrimSpace(msg.GetClientUri())
		if len(uri) > clientURIByteLimit {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_uri is too long"))
		}
		next.ClientURI = uri
	}
	if msg.GetReplaceRedirectUris() {
		if err := ValidateRedirectURIs(msg.GetRedirectUris()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		next.RedirectURIs = JoinRedirectURIs(msg.GetRedirectUris())
	}
	// The icon is validated BEFORE any write lands, because every other field
	// validation above answers before one does: a request whose icon bytes are
	// oversized or carry a disallowed media type must refuse the whole verb,
	// not commit the row rewrite and tear down channels first and report a
	// failure for a write that already happened.
	var icon []byte
	var iconMediaType string
	if msg.GetReplaceIcon() {
		var err error
		icon, iconMediaType, err = validateIcon(msg.GetIcon(), msg.GetIconMediaType())
		if err != nil {
			return nil, err
		}
	}
	if msg.GetReplaceScopes() {
		scopes, err := scopesFromProto(msg.GetScopes())
		if err != nil {
			return nil, err
		}
		if err := refuseAdminCeilingToNonAdmin(user, scopes); err != nil {
			return nil, err
		}
		// CLOSED, exactly as both registration builders close a fresh ceiling:
		// the stored column states the implications, so a consent stays a plain
		// subset test and an owner who unchecks an implied permission on the
		// edit screen cannot silently strip it from every credential the app
		// holds.
		stored, err := scopes.Close().Storable()
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		next.Scopes = stored
	}
	if msg.GetReplaceGrantTypes() {
		grants, err := normalizeGrantTypes(msg.GetGrantTypes())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		next.GrantTypes = strings.Join(grants, " ")
	}
	// The redirect list and the grant list are validated TOGETHER, against the
	// values that will be stored rather than the ones that arrived. A request
	// that adds authorization_code without a redirect address, and one that
	// removes the last address from an app that already has the grant, are the
	// same defect and the one shared rule catches both
	// (authorizationCodeNeedsRedirect, which the registrations also run).
	if authorizationCodeNeedsRedirect(strings.Fields(next.GrantTypes), ParseRedirectURIs(next.RedirectURIs)) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("the authorization_code grant needs at least one redirect URI"))
	}

	// Update and the icon write run in ONE transaction, for the same reason
	// RevokeApp runs its cascade in one: a SetIcon failure on a committed row
	// rewrite would report a verb as failed after half of it landed -- the
	// exact outcome the icon validation above exists to make unreachable.
	// The ceiling effects stay AFTER the commit; see applyCeilingChange.
	var rows int64
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		var err error
		rows, err = tx.OAuthClients().Update(ctx, next)
		if err != nil {
			return err
		}
		if rows == 0 {
			return store.ErrNotFound
		}
		if msg.GetReplaceIcon() {
			_, err = tx.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
				ClientID: app.ClientID, IconBlob: icon, IconMediaType: iconMediaType,
				CallerUserID: user.ID, CallerIsAdmin: user.IsAdmin,
			})
		}
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update app: %w", err))
	}

	s.applyCeilingChange(ctx, app.ClientID, app.Scopes, next.Scopes)

	// The response projects the row this handler just wrote through the one
	// statement of the editable field list (ApplyTo, beside the params type),
	// the icon state is known from the validation above, and nothing else
	// changed. Re-reading the row bought a second full-row fetch that could
	// only echo these values back.
	updated := *app
	next.ApplyTo(&updated)
	updated.UpdatedAt = s.now()
	if msg.GetReplaceIcon() {
		updated.HasIcon = len(icon) > 0
	}
	return connect.NewResponse(&leapmuxv1.UpdateAppResponse{
		App: appToProto(&updated, s.liveCredentialCount(ctx, updated.ClientID), s.verifierName(ctx, &updated)),
	}), nil
}

// applyCeilingChange lands an edit to the app's registered permission ceiling
// on the credentials it already holds.
//
// loadBearer narrows every stored grant to this ceiling at VALIDATION, so the
// new value is already authoritative -- but the 30-second cache holds the whole
// UserInfo, and an open Noise channel carries the scope set announced at its
// handshake. Neither notices a column that changed. This is the half that makes
// the edit land now rather than on the next cache miss, and never at all for a
// channel already open.
//
// PER CREDENTIAL, not per app, and that is the reason the ref carries the
// grant. A scope removed from the registration costs a channel teardown only
// for the credentials that actually held it; deciding once for the whole app
// would close every channel of a hub-wide app for accounts whose grant never
// reached the removed permission -- an outage in exchange for nothing.
// BearerRescoped picks each row's effect from the pair.
//
// BEST-EFFORT: the column is already written and validation re-reads it, so a
// failure here delays the effect by one cache window rather than losing it.
// Refusing the whole call would report a write that did happen as a failure.
func (s *AppService) applyCeilingChange(ctx context.Context, clientID, oldScopes, newScopes string) {
	if oldScopes == newScopes {
		return
	}
	before, beforeErr := authscope.Parse(oldScopes)
	after, afterErr := authscope.Parse(newScopes)
	if beforeErr != nil || afterErr != nil {
		slog.WarnContext(ctx, "could not read an app's permission ceiling either side of an edit",
			"client_id", clientID, "before_err", beforeErr, "after_err", afterErr)
		return
	}
	refs, err := s.store.OAuthClients().ListTokenRefs(ctx, clientID)
	if err != nil {
		slog.WarnContext(ctx, "could not read the credentials of an app whose permission ceiling changed",
			"client_id", clientID, "err", err)
		return
	}
	// ONE batched effect for every credential the edit moved: the widen or
	// narrow decision is stated once inside the batch, and the validation hot
	// path's mutex pays two lock cycles for the whole set -- one per effect
	// class -- rather than one per credential.
	ops := make([]auth.RescopeOp, 0, len(refs))
	for _, ref := range refs {
		granted, err := authscope.Parse(ref.GrantedScopes)
		if err != nil {
			// An unreadable grant already refuses the credential at
			// validation, so there is nothing live to withdraw.
			continue
		}
		ops = append(ops, auth.RescopeOp{
			TokenID: ref.ID,
			Before:  granted.NarrowTo(before),
			After:   granted.NarrowTo(after),
		})
	}
	s.lifecycle.BearerRescopedBatch(auth.BearerKindAPI, ops)
}

// SetAppElevationAllowed toggles the step-up stage.
//
// It is its own verb, and a BUILT-IN registration accepts it although it
// accepts no other edit: an operator who does not want `leapmux control admin`
// to elevate must be able to say so.
//
// Elevated, and turning the flag ON is why: it hands an app the ceremony that
// multiplies every scope it holds. See requireElevatedOwner.
func (s *AppService) SetAppElevationAllowed(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SetAppElevationAllowedRequest],
) (*connect.Response[leapmuxv1.SetAppElevationAllowedResponse], error) {
	user, err := s.requireElevatedOwner(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.loadOwned(ctx, req.Msg.GetClientId(), user)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
		ClientID:         app.ClientID,
		ElevationAllowed: req.Msg.GetAllowed(),
		CallerUserID:     user.ID,
		CallerIsAdmin:    user.IsAdmin,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("set elevation allowed: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
	}
	// The 30-second validation cache holds the whole UserInfo, elevation
	// included, so the row this write just changed is not what the next request
	// reads. loadBearer zeroes a live window when the app lacks the flag -- but
	// only on a cache MISS, so without this the withdrawal lands up to a window
	// late, on exactly the write an owner makes because they stopped trusting
	// the app.
	//
	// Cache-only, in both directions: an elevation window controls Hub-side
	// writes and is never announced into a Noise channel, so there is nothing
	// open to tear down. See BearerElevationPolicyChanged.
	//
	// The read is BEST-EFFORT: the row is already written and the flag is
	// re-read at every validation, so a failure here delays the withdrawal by
	// one cache window rather than losing it. Refusing the whole call would
	// report a write that did happen as a failure.
	//
	// EVERY user's credentials, which is what the empty UserID selects. The
	// flag is a property of the APP, so an owner turning it off withdraws the
	// window from every account that authorized the app -- not only from the
	// one making this call.
	//
	// SKIPPED when the flag did not move: a cached UserInfo validated under
	// the current flag already agrees with the row, so there is no window to
	// withdraw and the loop below would pay one token-ref read and one lock
	// cycle per credential for nothing. The one divergence is a concurrent
	// opposite toggle landing between this handler's load and its write,
	// whose cached UserInfo can then run one window stale -- the same
	// one-cache-window delay a failed read above already costs, and the flag
	// is re-read at the next miss.
	if req.Msg.GetAllowed() != app.ElevationAllowed {
		refs, refErr := s.store.OAuthClients().ListTokenRefs(ctx, app.ClientID)
		if refErr != nil {
			slog.WarnContext(ctx, "could not drop the cached credentials of an app whose elevation policy changed",
				"client_id", app.ClientID, "err", refErr)
		}
		ids := make([]string, 0, len(refs))
		for _, ref := range refs {
			ids = append(ids, ref.ID)
		}
		// ONE batched invalidation: a single lock acquisition and generation
		// bump for the whole app, rather than one lock cycle per credential.
		s.lifecycle.BearerElevationPolicyChangedBatch(auth.BearerKindAPI, ids)
	}

	updated := *app
	updated.ElevationAllowed = req.Msg.GetAllowed()
	updated.UpdatedAt = s.now()
	return connect.NewResponse(&leapmuxv1.SetAppElevationAllowedResponse{
		App: appToProto(&updated, s.liveCredentialCount(ctx, updated.ClientID), s.verifierName(ctx, &updated)),
	}), nil
}

// VerifyApp records an administrator's vouch, or withdraws one.
//
// Administrators only, and the interceptor's admin check does NOT cover this
// service -- AppService is not an admin service, because an ordinary user
// registers apps through it. So the check is here, stated rather than assumed.
//
// Elevated: a vouch removes the "unverified" label the consent screen shows,
// which is the one signal a person has that an app is not what it says. See
// requireElevatedOwner.
func (s *AppService) VerifyApp(
	ctx context.Context,
	req *connect.Request[leapmuxv1.VerifyAppRequest],
) (*connect.Response[leapmuxv1.VerifyAppResponse], error) {
	user, err := s.requireElevatedOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !user.IsAdmin {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("only an administrator can verify an app"))
	}
	app, err := s.load(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, err
	}
	// A built-in is verified by construction (ClientIsVerified answers true
	// for the builtin source), so a vouch on one states nothing and a
	// withdrawal contradicts it -- the same "constants of the build" rule
	// every other write on these rows applies.
	if err := refuseBuiltInEdit(app); err != nil {
		return nil, err
	}
	// Both fields move together, which is what the half-vouch CHECK enforces
	// at the column. Setting them from one branch is what keeps a caller from
	// ever writing half of it.
	params := store.SetOAuthClientVerifiedParams{ClientID: app.ClientID, CallerIsAdmin: user.IsAdmin}
	if req.Msg.GetVerified() {
		now := s.now().UTC()
		params.VerifiedAt, params.VerifiedBy = &now, user.ID.String()
	}
	// The statement filters revoked_at IS NULL, and s.load above deliberately
	// reads a revoked row: without the rows check, a vouch on an app somebody
	// retired a moment ago would answer success and project the vouch onto a
	// row that never took it -- the exact lie the sibling write verbs refuse.
	rows, err := s.store.OAuthClients().SetVerified(ctx, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("set app verified: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
	}
	updated := *app
	updated.UpdatedAt = s.now()
	if req.Msg.GetVerified() {
		updated.VerifiedAt = params.VerifiedAt
		updated.VerifiedBy = params.VerifiedBy
	} else {
		updated.VerifiedAt = nil
		updated.VerifiedBy = ""
	}
	return connect.NewResponse(&leapmuxv1.VerifyAppResponse{
		App: appToProto(&updated, s.liveCredentialCount(ctx, updated.ClientID), s.verifierName(ctx, &updated)),
	}), nil
}

// RevokeApp retires the app and takes every credential it holds.
//
// NO elevation, and requireElevatedOwner states why: it only reduces access, and it is
// what somebody reaches for on realizing an app is malicious.
//
// The credential rows are READ before the cascade so their lifecycle effects
// can run after the transaction commits: effects accumulate, and the store may
// retry a transaction, so running them inside would apply some of them twice.
func (s *AppService) RevokeApp(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RevokeAppRequest],
) (*connect.Response[leapmuxv1.RevokeAppResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.loadOwned(ctx, req.Msg.GetClientId(), user)
	if err != nil {
		return nil, err
	}
	if err := refuseBuiltInEdit(app); err != nil {
		return nil, err
	}

	refs, err := s.store.OAuthClients().ListTokenRefs(ctx, app.ClientID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list app credentials: %w", err))
	}

	var revoked int64
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		rows, err := tx.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
			ClientID: app.ClientID, CallerUserID: user.ID, CallerIsAdmin: user.IsAdmin,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return store.ErrNotFound
		}
		revoked, err = tx.OAuthClients().RevokeTokens(ctx, app.ClientID)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke app: %w", err))
	}

	// AFTER the commit, so a retried transaction cannot apply these twice.
	// ONE batched eviction for the whole set, with the channel sweep per row
	// inside the batch.
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	s.lifecycle.BearerRevokedBatch(auth.BearerKindAPI, ids)
	return connect.NewResponse(&leapmuxv1.RevokeAppResponse{RevokedCredentialCount: revoked}), nil
}

// DeleteApp hard-deletes a registration that never held a credential.
//
// NO elevation: the foreign key refuses it while any credential row exists, so
// the reduction already happened through RevokeApp and what is left is an
// empty record. See requireElevatedOwner.
//
// The foreign key refuses it otherwise, and this reports WHY rather than
// surfacing a constraint error: an operator told "delete failed" cannot act,
// and one told "it holds four live credentials" can revoke instead.
func (s *AppService) DeleteApp(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteAppRequest],
) (*connect.Response[leapmuxv1.DeleteAppResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.loadOwned(ctx, req.Msg.GetClientId(), user)
	if err != nil {
		return nil, err
	}
	if err := refuseBuiltInEdit(app); err != nil {
		return nil, err
	}
	// EVERY credential row, revoked ones included, because that is what the
	// RESTRICT foreign key counts. Asking for the live count told an operator
	// to revoke and then refused the delete anyway, with a constraint error
	// that states a table they have no surface for.
	held, err := s.store.OAuthClients().CountTokens(ctx, app.ClientID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count app credentials: %w", err))
	}
	if held > 0 {
		live, err := s.store.OAuthClients().CountLiveTokens(ctx, app.ClientID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count live app credentials: %w", err))
		}
		// The two numbers say different things and the operator needs both:
		// live credentials mean "revoke it instead", and revoked ones mean
		// "this app has a history, so it cannot be erased".
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"this app holds %d credential(s), %d of them live; revoke it instead of deleting it", held, live))
	}
	rows, err := s.store.OAuthClients().Delete(ctx, store.OAuthClientOwnershipParams{
		ClientID: app.ClientID, CallerUserID: user.ID, CallerIsAdmin: user.IsAdmin,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete app: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
	}
	return connect.NewResponse(&leapmuxv1.DeleteAppResponse{}), nil
}

// --- helpers ---------------------------------------------------------------

func (s *AppService) snapshot(ctx context.Context) *settings.Snapshot {
	return settingsSnapshotOf(s.settings, ctx)
}

// resolveOwnership turns the requested visibility into the two columns that
// record it, and refuses a hub-wide registration to a non-administrator.
//
// APP_VISIBILITY_UNSPECIFIED means PRIVATE. The narrower answer is the safe
// default for an omitted field, and it is the only one a non-administrator
// can mean.
func (s *AppService) resolveOwnership(
	visibility leapmuxv1.AppVisibility, user *auth.UserInfo,
) (owner, source string, err error) {
	if visibility == leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE {
		if !user.IsAdmin {
			return "", "", connect.NewError(connect.CodePermissionDenied,
				errors.New("only an administrator can register a hub-wide app"))
		}
		// An empty owner IS hub-wide. One column carries the whole visibility
		// rule, so there is no second flag to disagree with it.
		return "", store.OAuthClientSourceAdmin, nil
	}
	return user.ID.String(), store.OAuthClientSourceUser, nil
}

// buildAppParams turns a RegisterAppRequest into the row the shared core
// validates and derives. The identified surfaces differ from RFC 7591 in
// exactly
// three ways the spec carries: the icon the registrant uploaded, the
// elevation flag the OWNER asked for, and a confidentiality the client type
// states directly. Every validation rule is the core's, so a redirect-URI
// rule or a ceiling closure holds on all surfaces or neither.
func (s *AppService) buildAppParams(
	msg *leapmuxv1.RegisterAppRequest, owner, createdBy, source string, scopes authscope.ScopeSet,
) (store.CreateOAuthClientParams, string, error) {
	params, secret, err := buildOAuthClientRegistration(s.validator, appRegistrationSpec{
		name:         msg.GetClientName(),
		clientURI:    msg.GetClientUri(),
		redirectURIs: msg.GetRedirectUris(),
		grantTypes:   msg.GetGrantTypes(),
		scopes:       scopes,
		confidential: msg.GetClientType() == leapmuxv1.AppClientType_APP_CLIENT_TYPE_CONFIDENTIAL,
		// The registrant asks, and the OWNER decides. For a private app those
		// are the same person, so the request is honored; for a hub-wide one
		// the registrant is an administrator, which is the same answer.
		elevationAllowed: msg.GetElevationAllowed(),
		icon:             msg.GetIcon(),
		iconMediaType:    msg.GetIconMediaType(),
	}, source, owner, createdBy)
	if err != nil {
		// The Connect surface answers one code with the core's own text: the
		// distinction between invalid_redirect_uri and
		// invalid_client_metadata is RFC 7591 vocabulary.
		return store.CreateOAuthClientParams{}, "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	return params, secret, nil
}

// refuseAdminCeilingToNonAdmin states the one refusal both the register and
// the edit verbs make: an app whose CEILING reaches hub administration needs
// an administrator, whatever the account later consents to. Without it, a
// user could register a private app asking for admin:users and the refusal
// would arrive at the consent screen -- after the app exists and its operator
// was told it was registered.
func refuseAdminCeilingToNonAdmin(user *auth.UserInfo, scopes authscope.ScopeSet) error {
	if user.IsAdmin {
		return nil
	}
	if scope, found := firstAdminScope(scopes); found {
		token, _ := authscope.Token(scope)
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("your account is not a hub administrator, so an app you register cannot ask for %s", token))
	}
	return nil
}

// load reads one app, whatever its state. A revoked app stays readable because
// a live credential on a retired app must still be explainable.
func (s *AppService) load(ctx context.Context, clientID string) (*store.OAuthClient, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_id is required"))
	}
	app, err := s.store.OAuthClients().Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get app: %w", err))
	}
	return app, nil
}

// loadOwned reads an app the caller may edit, and answers NOT FOUND when they
// may not.
//
// Not-found rather than permission-denied, and deliberately: a private app is
// invisible to everybody but its owner, so telling a stranger "that exists but
// is not yours" would answer a question the visibility rule exists to refuse.
//
// It is NOT the authorization guard. Each write carries the caller into its own
// statement, so this read is what produces a good message rather than what
// protects the row.
func (s *AppService) loadOwned(
	ctx context.Context, clientID string, user *auth.UserInfo,
) (*store.OAuthClient, error) {
	app, err := s.load(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if !assertAppOwner(app, user) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such app"))
	}
	return app, nil
}

// refuseBuiltInEdit protects the two rows the build itself seeds
// (store.SeedBuiltIns, at every store open).
//
// Their fields are constants of the BUILD -- the control CLI's redirect address
// and the service account's absent one -- so an edit would leave the database
// disagreeing with the binary that re-states them on its next boot.
// SetAppElevationAllowed is the one exception and does not call this: the seed
// reconciles the constants and never the operator's flag.
func refuseBuiltInEdit(app *store.OAuthClient) error {
	if app.RegistrationSource != store.OAuthClientSourceBuiltin {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this app ships with the hub; only its elevation setting can change"))
}

// firstAdminScope reports the first hub-administration scope in a set, so a
// refusal can NAME the one that caused it.
func firstAdminScope(set authscope.ScopeSet) (leapmuxv1.Scope, bool) {
	for _, scope := range adminScopeList {
		if set.Allows(scope) {
			return scope, true
		}
	}
	return leapmuxv1.Scope_SCOPE_UNSPECIFIED, false
}

// scopesFromProto turns the wire enum list into a set, refusing the whole list
// on a value no account can grant.
//
// The refusal is TOTAL rather than a filter, for the reason the stored grant
// takes the same line: a request holding one good scope and one impossible one
// is a caller that meant something the hub cannot express, and silently
// registering the readable half hides that.
func scopesFromProto(wire []leapmuxv1.Scope) (authscope.ScopeSet, error) {
	if len(wire) == 0 {
		return authscope.ScopeSet{}, connect.NewError(connect.CodeInvalidArgument, errAppNeedsScope)
	}
	set, ok := authscope.ScopesFromWire(wire)
	if !ok {
		return authscope.ScopeSet{}, connect.NewError(connect.CodeInvalidArgument,
			errors.New("the requested permissions include one this hub cannot grant"))
	}
	if set.IsUnscoped() {
		// SCOPE_ALL is the explicit absence of a limit, which no registration
		// may claim: an app's ceiling is what a consent screen shows, and
		// "everything, including whatever is added later" cannot be shown.
		return authscope.ScopeSet{}, connect.NewError(connect.CodeInvalidArgument,
			errors.New("an app must specify the permissions it wants"))
	}
	return set, nil
}

// validateIcon caps the stored bytes and requires a media type to go with them.
//
// EMPTY bytes clear the icon, which is why the media type is only required
// alongside real bytes. The consent page then renders a monogram, which fetches
// nothing at all.
func validateIcon(icon []byte, mediaType string) ([]byte, string, error) {
	if len(icon) == 0 {
		return nil, "", nil
	}
	// maxIconBytes, shared with the RFC 7591 endpoint. Two caps on the same
	// column would be two answers to one question.
	if len(icon) > maxIconBytes {
		return nil, "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the icon is larger than %d bytes", maxIconBytes))
	}
	mediaType = strings.TrimSpace(mediaType)
	if !isAllowedIconMediaType(mediaType) {
		return nil, "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the icon media type must be one of %s", strings.Join(AllowedIconMediaTypes(), ", ")))
	}
	return icon, mediaType, nil
}

// appToProto maps a stored registration to the wire shape.
//
// It is PURE: the live count and the verifier's name arrive as arguments, so
// a listing batches them into one round trip each while a single-app verb pays
// one, and a mapper that performed I/O per row is the N+1 this file used to
// carry.
//
// It never carries the secret or its hash: the secret crosses once, in
// RegisterAppResponse, and TestAppCarriesNoSecret pins that this shape holds no
// field that could.
func appToProto(app *store.OAuthClient, liveCount int64, verifiedBy string) *leapmuxv1.App {
	// An unreadable ceiling renders as NO permissions rather than as the raw
	// string, the same answer appScopeCeiling gives on the consent stage: a set
	// a person cannot interpret must not appear as though it were one they
	// can, and the drift is logged in that one helper rather than twice.
	scopes := appScopeCeiling(app)
	visibility := leapmuxv1.AppVisibility_APP_VISIBILITY_PRIVATE
	if app.IsHubWide() {
		visibility = leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE
	}
	clientType := leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC
	if len(app.SecretHash) > 0 {
		clientType = leapmuxv1.AppClientType_APP_CLIENT_TYPE_CONFIDENTIAL
	}
	out := &leapmuxv1.App{
		ClientId:           app.ClientID,
		ClientName:         app.ClientName,
		ClientUri:          app.ClientURI,
		Visibility:         visibility,
		ClientType:         clientType,
		RedirectUris:       ParseRedirectURIs(app.RedirectURIs),
		Scopes:             authscope.ScopesToWire(scopes),
		GrantTypes:         strings.Fields(app.GrantTypes),
		ElevationAllowed:   app.ElevationAllowed,
		RegistrationSource: app.RegistrationSource,
		HasIcon:            app.HasIcon,
		// The one verified rule, stated through the same predicate the consent
		// page and icon endpoint read: a vouch or a built-in of this build.
		Verified:  app.IsVerified(),
		CreatedAt: timestamppb.New(app.CreatedAt),
		UpdatedAt: timestamppb.New(app.UpdatedAt),
	}
	if app.VerifiedAt != nil {
		out.VerifiedAt = timestamppb.New(*app.VerifiedAt)
		out.VerifiedByUsername = verifiedBy
	}
	if app.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*app.RevokedAt)
	}
	out.LiveCredentialCount = liveCount
	return out
}

// liveCredentialCount reads one app's live credential count, for the
// single-app verbs; the listing batches the same question through
// CountLiveTokensByClients.
func (s *AppService) liveCredentialCount(ctx context.Context, clientID string) int64 {
	counts, err := s.store.OAuthClients().CountLiveTokensByClients(ctx, []string{clientID})
	if err != nil {
		// A count that cannot be read is not a reason to fail a write that
		// already landed; the listing refreshes it on the next open.
		return 0
	}
	return counts[clientID]
}

// verifierName resolves the vouching administrator's name for one response.
func (s *AppService) verifierName(ctx context.Context, app *store.OAuthClient) string {
	if app.VerifiedAt == nil {
		return ""
	}
	return s.usernameOf(ctx, app.VerifiedBy)
}

// usernameOf resolves a vouching administrator's name for display.
//
// A failed lookup answers EMPTY rather than an error. The vouch itself is the
// fact the consent page and the app list depend on; who recorded it is a label,
// and a deleted administrator (the column is ON DELETE SET NULL) must not make
// the app unreadable.
func (s *AppService) usernameOf(ctx context.Context, userID string) string {
	if strings.TrimSpace(userID) == "" {
		return ""
	}
	u, err := s.store.Users().GetByID(ctx, userID)
	if err != nil || u == nil {
		return ""
	}
	return u.Username
}

// usernameOfMemoized is usernameOf behind a page-sized memo, so a listing pays
// one lookup per DISTINCT vouching administrator rather than one per row.
//
// It MUTATES the caller's map and returns only the name: the memo stays owned
// by the loop that allocates it, and a caller cannot "re-initialize" what it
// hands back per row.
func (s *AppService) usernameOfMemoized(ctx context.Context, userID string, memo map[string]string) string {
	if name, ok := memo[userID]; ok {
		return name
	}
	name := s.usernameOf(ctx, userID)
	memo[userID] = name
	return name
}
