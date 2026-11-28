package service

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminIdPService implements the leapmux.v1.AdminIdPService ConnectRPC
// handler.
//
// Every WRITE here runs under writeUnderElevation, the same gate every
// hub-settings write takes. An identity provider row is hub configuration and
// carries more than a settings key does: it installs a sign-in route for the
// whole hub, and one with trust_email set links an incoming identity to any
// account whose verified address it presents. Removing the last one locks out
// every account that has no other login method. OAuth issuer validation runs
// for every OIDC-typed provider (generic `oidc` as well as the `google` and
// `apple` presets) by fetching its discovery document; non-OIDC providers
// (`github`) skip it.
type AdminIdPService struct {
	store store.Store
	// cache drops the built instances of a provider this service removes,
	// disables, or rotates the client secret of. loadEnabledProvider refuses a
	// deleted row with 404 and a disabled row with 403 BEFORE it rebuilds, so
	// the login handler's own eviction can never reach such an entry -- and
	// the entry holds the client secret decrypted by the store. Secret rotation
	// evicts cached clients without clearing in-flight login states or pending
	// signups. The dependency is an interface so the admin verbs need the
	// eviction and not the login handler.
	cache providerCacheInvalidator
	// clockSeam supplies the instant the elevation gate and its slide read.
	// Nil means time.Now, so production wires nothing.
	clockSeam
}

// providerCacheInvalidator is what the admin verbs need from the OAuth login
// handler: full invalidation (for remove/disable) and client-only eviction (for
// secret rotation).
type providerCacheInvalidator interface {
	InvalidateProvider(providerID string)
	EvictProviderCache(providerID string)
}

func NewAdminIdPService(st store.Store, cache providerCacheInvalidator) *AdminIdPService {
	if cache == nil {
		panic("admin idp service requires a provider cache invalidator")
	}
	return &AdminIdPService{store: st, cache: cache}
}

func adminOAuthProviderToProto(p store.OAuthProviderSummary) *leapmuxv1.AdminOAuthProvider {
	return &leapmuxv1.AdminOAuthProvider{
		Id:           p.ID,
		ProviderType: oauth.ProviderTypeWire(p.ProviderType),
		Name:         p.Name,
		IssuerUrl:    p.IssuerURL,
		ClientId:     p.ClientID,
		Scopes:       p.Scopes,
		TrustEmail:   p.TrustEmail,
		ResponseMode: oauth.ResponseModeWire(p.ResponseMode),
		Enabled:      p.Enabled,
		CreatedAt:    timestamppb.New(p.CreatedAt),
	}
}

func (s *AdminIdPService) AddOAuthProvider(ctx context.Context, req *leapmuxv1.AddOAuthProviderRequest) (*leapmuxv1.AddOAuthProviderResponse, error) {
	msg := req
	if msg.GetProviderType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "provider_type is required (github, google, apple, oidc)")
	}
	if err := requireField(msg.GetClientId(), "client_id"); err != nil {
		return nil, err
	}
	if err := requireField(msg.GetClientSecret(), "client_secret"); err != nil {
		return nil, err
	}

	preset, ok := oauth.Presets[msg.GetProviderType()]
	if !ok {
		return nil, connect.Errorf(connect.CodeInvalidArgument,
			"unknown provider type: %s (supported: github, google, apple, oidc)", msg.GetProviderType())
	}
	displayName := msg.GetName()
	if displayName == "" {
		displayName = preset.Name
	}
	if displayName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required for generic OIDC providers")
	}
	storedType := preset.ProviderType
	issuer := msg.GetIssuerUrl()
	if issuer == "" {
		issuer = preset.IssuerURL
	}
	scopes := msg.GetScopes()
	if scopes == "" {
		scopes = preset.Scopes
	}
	// trust_email: explicit flag > preset default > refused for generic OIDC.
	var trustEmail *bool
	if msg.TrustEmail != nil {
		trustEmail = msg.TrustEmail
	} else {
		trustEmail = preset.TrustEmail
	}
	if trustEmail == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "trust_email is required for generic OIDC providers")
	}
	responseMode := preset.ResponseMode
	if rawMode := msg.GetResponseMode(); rawMode != "" {
		parsedMode, err := oauth.ParseResponseMode(rawMode)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		responseMode = parsedMode
	}
	if storedType == oauth.ProviderTypeGitHub && responseMode == oauth.ResponseModeFormPost {
		return nil, connect.NewError(connect.CodeInvalidArgument, "form_post response_mode is only supported for OIDC providers")
	}

	// Issuer discovery validation runs for every OIDC-typed provider (including
	// the google and apple presets); non-OIDC providers (github) skip it.
	if storedType == oauth.ProviderTypeOIDC {
		if issuer == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "issuer_url is required for OIDC providers")
		}
		if err := oauth.ValidateIssuer(ctx, issuer); err != nil {
			return nil, wrappedConnectError(connect.CodeInvalidArgument, fmt.Errorf("issuer validation failed: %w", err))
		}
	}

	providerID := id.Generate()
	if err := writeUnderElevation(ctx, s.store, s.now, func(tx store.Store) error {
		if err := tx.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID:           providerID,
			ProviderType: storedType,
			Name:         displayName,
			IssuerURL:    issuer,
			ClientID:     msg.GetClientId(),
			ClientSecret: []byte(msg.GetClientSecret()),
			Scopes:       scopes,
			TrustEmail:   *trustEmail,
			ResponseMode: responseMode,
			Enabled:      true,
		}); err != nil {
			return storeConnectError(err, "create provider")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	created, err := s.store.OAuthProviders().GetByID(ctx, providerID)
	if err != nil {
		return nil, storeConnectError(err, "get created provider")
	}
	return &leapmuxv1.AddOAuthProviderResponse{
		Provider: adminOAuthProviderToProto(created.OAuthProviderSummary),
	}, nil
}

func (s *AdminIdPService) ListOAuthProviders(ctx context.Context, req *leapmuxv1.ListOAuthProvidersRequest) (*leapmuxv1.ListOAuthProvidersResponse, error) {
	var (
		providers []store.OAuthProviderSummary
		err       error
	)
	if strings.TrimSpace(req.GetIdPrefix()) != "" {
		providers, err = s.store.OAuthProviders().ListByIDPrefix(ctx, req.GetIdPrefix())
	} else {
		providers, err = s.store.OAuthProviders().ListAll(ctx)
	}
	if err != nil {
		return nil, storeConnectError(err, "list providers")
	}
	out := make([]*leapmuxv1.AdminOAuthProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, adminOAuthProviderToProto(p))
	}
	return &leapmuxv1.ListOAuthProvidersResponse{Providers: out}, nil
}

// requireOAuthProvider refuses an empty or unknown provider id.
//
// Both write verbs need it, for the same reason: their UPDATE and DELETE
// match no row and report no error, so without the check the hub tells an
// operator who mistypes an id that the login method is removed or disabled,
// while it stays enabled for every user.
func (s *AdminIdPService) requireOAuthProvider(ctx context.Context, id string) error {
	if err := requireField(id, "id"); err != nil {
		return err
	}
	if _, err := s.store.OAuthProviders().GetByID(ctx, id); err != nil {
		return storeConnectError(err, "get provider")
	}
	return nil
}

// RemoveOAuthProvider deletes the provider row, which cascades every
// account link to it away (ON DELETE CASCADE, all three dialects).
//
// An account with no password whose only link was this provider therefore
// loses its last login method, and nothing but `leapmux recover` brings it
// back. The hub already refuses that outcome for one account
// (UserService.UnlinkOAuthProvider) and puts the analogous loss behind
// force (AdminUserService.DeleteUser), so this verb takes the same shape:
// refuse with the count, and admit the removal when the caller passes force.
//
// The count travels in the response either way. An operator who passes
// force learns exactly how many accounts the removal locked out, and an
// operator on a provider nobody depends on reads zero.
func (s *AdminIdPService) RemoveOAuthProvider(ctx context.Context, req *leapmuxv1.RemoveOAuthProviderRequest) (*leapmuxv1.RemoveOAuthProviderResponse, error) {
	if err := s.requireOAuthProvider(ctx, req.GetId()); err != nil {
		return nil, err
	}
	orphaned, err := s.store.OAuthUserLinks().CountUsersOrphanedByProvider(ctx, req.GetId())
	if err != nil {
		return nil, storeConnectError(err, "count users orphaned by provider")
	}
	if orphaned > 0 && !req.GetForce() {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"%d user(s) have no other login method through provider %q; they will be locked out - set a password for them first, or pass force",
			orphaned, req.GetId())
	}
	if err := writeUnderElevation(ctx, s.store, s.now, func(tx store.Store) error {
		if err := tx.OAuthProviders().Delete(ctx, req.GetId()); err != nil {
			return storeConnectError(err, "delete provider")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	s.cache.InvalidateProvider(req.GetId())
	return &leapmuxv1.RemoveOAuthProviderResponse{LockedOutUsers: orphaned}, nil
}

func (s *AdminIdPService) SetOAuthProviderEnabled(ctx context.Context, req *leapmuxv1.SetOAuthProviderEnabledRequest) (*leapmuxv1.SetOAuthProviderEnabledResponse, error) {
	if err := s.requireOAuthProvider(ctx, req.GetId()); err != nil {
		return nil, err
	}
	if err := writeUnderElevation(ctx, s.store, s.now, func(tx store.Store) error {
		if err := tx.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
			Enabled: req.GetEnabled(),
			ID:      req.GetId(),
		}); err != nil {
			return storeConnectError(err, "update provider")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Unconditional, for both directions. A re-enable then rebuilds once on
	// the next request, and one rule reads better than two.
	s.cache.InvalidateProvider(req.GetId())
	return &leapmuxv1.SetOAuthProviderEnabledResponse{}, nil
}

// UpdateOAuthProviderSecret replaces the encrypted client secret on an
// existing identity provider without changing its ID or disturbing linked user
// accounts, then evicts cached provider clients so subsequent sign-ins use the
// new secret immediately without aborting in-flight signups.
func (s *AdminIdPService) UpdateOAuthProviderSecret(ctx context.Context, req *leapmuxv1.UpdateOAuthProviderSecretRequest) (*leapmuxv1.UpdateOAuthProviderSecretResponse, error) {
	if err := requireField(req.GetId(), "id"); err != nil {
		return nil, err
	}
	if err := requireField(req.GetClientSecret(), "client_secret"); err != nil {
		return nil, err
	}
	if err := writeUnderElevation(ctx, s.store, s.now, func(tx store.Store) error {
		if err := tx.OAuthProviders().UpdateClientSecret(ctx, store.UpdateOAuthProviderClientSecretParams{
			ID:           req.GetId(),
			ClientSecret: []byte(req.GetClientSecret()),
		}); err != nil {
			return storeConnectError(err, "update provider secret")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	s.cache.EvictProviderCache(req.GetId())
	return &leapmuxv1.UpdateOAuthProviderSecretResponse{}, nil
}
