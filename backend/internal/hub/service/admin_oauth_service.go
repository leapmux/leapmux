package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminOAuthService implements the leapmux.v1.AdminOAuthService ConnectRPC
// handler. OAuth issuer validation stays conditional exactly as the CLI
// verb had it: OIDC-typed providers validate their issuer by fetching its
// discovery document; presets (github/google/apple) skip it.
type AdminOAuthService struct {
	store store.Store
	ks    *keystore.Keystore
	// cache drops the built instances of a provider this service removes or
	// disables. loadEnabledProvider refuses a deleted row with 404 and a
	// disabled row with 403 BEFORE it rebuilds, so the login handler's own
	// eviction can never reach such an entry -- and the entry holds the
	// client secret the keystore decrypted. The dependency is an interface
	// so the admin verbs need the eviction and not the login handler.
	cache providerCacheInvalidator
}

// providerCacheInvalidator is the one thing the admin verbs need from the
// OAuth login handler.
type providerCacheInvalidator interface {
	InvalidateProvider(providerID string)
}

func NewAdminOAuthService(st store.Store, ks *keystore.Keystore, cache providerCacheInvalidator) *AdminOAuthService {
	if cache == nil {
		panic("admin oauth service requires a provider cache invalidator")
	}
	return &AdminOAuthService{store: st, ks: ks, cache: cache}
}

func adminOAuthProviderToProto(p store.OAuthProviderSummary) *leapmuxv1.AdminOAuthProvider {
	return &leapmuxv1.AdminOAuthProvider{
		Id:           p.ID,
		ProviderType: p.ProviderType,
		Name:         p.Name,
		IssuerUrl:    p.IssuerURL,
		ClientId:     p.ClientID,
		Scopes:       p.Scopes,
		TrustEmail:   p.TrustEmail,
		Enabled:      p.Enabled,
		CreatedAt:    timestamppb.New(p.CreatedAt),
	}
}

func (s *AdminOAuthService) AddOAuthProvider(ctx context.Context, req *connect.Request[leapmuxv1.AddOAuthProviderRequest]) (*connect.Response[leapmuxv1.AddOAuthProviderResponse], error) {
	msg := req.Msg
	if msg.GetProviderType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider_type is required (github, google, apple, oidc)"))
	}
	if msg.GetClientId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_id is required"))
	}
	if msg.GetClientSecret() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_secret is required"))
	}

	preset, ok := oauth.Presets[msg.GetProviderType()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown provider type: %s (supported: github, google, apple, oidc)", msg.GetProviderType()))
	}
	displayName := msg.GetName()
	if displayName == "" {
		displayName = preset.Name
	}
	if displayName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required for generic OIDC providers"))
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
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("trust_email is required for generic OIDC providers"))
	}

	// Issuer validation is conditional: OIDC-typed providers only; presets
	// carry a known-good issuer.
	if storedType == oauth.ProviderTypeOIDC {
		if issuer == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("issuer_url is required for OIDC providers"))
		}
		if err := oauth.ValidateIssuer(ctx, issuer); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("issuer validation failed: %w", err))
		}
	}

	if s.ks == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("keystore is not configured"))
	}
	providerID := id.Generate()
	encryptedSecret, err := s.ks.Encrypt([]byte(msg.GetClientSecret()), keystore.ProviderAAD(providerID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encrypt client secret: %w", err))
	}
	if err := s.store.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: storedType,
		Name:         displayName,
		IssuerURL:    issuer,
		ClientID:     msg.GetClientId(),
		ClientSecret: encryptedSecret,
		Scopes:       scopes,
		TrustEmail:   *trustEmail,
		Enabled:      true,
	}); err != nil {
		return nil, storeConnectError(err, "create provider")
	}
	created, err := s.store.OAuthProviders().GetByID(ctx, providerID)
	if err != nil {
		return nil, storeConnectError(err, "get created provider")
	}
	return connect.NewResponse(&leapmuxv1.AddOAuthProviderResponse{
		Provider: adminOAuthProviderToProto(created.OAuthProviderSummary),
	}), nil
}

func (s *AdminOAuthService) ListOAuthProviders(ctx context.Context, _ *connect.Request[leapmuxv1.ListOAuthProvidersRequest]) (*connect.Response[leapmuxv1.ListOAuthProvidersResponse], error) {
	providers, err := s.store.OAuthProviders().ListAll(ctx)
	if err != nil {
		return nil, storeConnectError(err, "list providers")
	}
	out := make([]*leapmuxv1.AdminOAuthProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, adminOAuthProviderToProto(p))
	}
	return connect.NewResponse(&leapmuxv1.ListOAuthProvidersResponse{Providers: out}), nil
}

// requireOAuthProvider refuses an empty or unknown provider id.
//
// Both write verbs need it, for the same reason: their UPDATE and DELETE
// match no row and report no error, so without the check an operator who
// mistypes an id is told the login method is removed or disabled while it
// stays enabled for every user.
func (s *AdminOAuthService) requireOAuthProvider(ctx context.Context, id string) error {
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
// refuse with the count, and let force through.
//
// The count travels in the response either way. An operator who passes
// force learns exactly how many accounts the removal locked out, and an
// operator on a provider nobody depends on reads zero.
func (s *AdminOAuthService) RemoveOAuthProvider(ctx context.Context, req *connect.Request[leapmuxv1.RemoveOAuthProviderRequest]) (*connect.Response[leapmuxv1.RemoveOAuthProviderResponse], error) {
	if err := s.requireOAuthProvider(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	orphaned, err := s.store.OAuthUserLinks().CountUsersOrphanedByProvider(ctx, req.Msg.GetId())
	if err != nil {
		return nil, storeConnectError(err, "count users orphaned by provider")
	}
	if orphaned > 0 && !req.Msg.GetForce() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%d user(s) have no other login method through provider %q; they will be locked out - set a password for them first, or pass force",
				orphaned, req.Msg.GetId()))
	}
	if err := s.store.OAuthProviders().Delete(ctx, req.Msg.GetId()); err != nil {
		return nil, storeConnectError(err, "delete provider")
	}
	s.cache.InvalidateProvider(req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.RemoveOAuthProviderResponse{LockedOutUsers: orphaned}), nil
}

func (s *AdminOAuthService) SetOAuthProviderEnabled(ctx context.Context, req *connect.Request[leapmuxv1.SetOAuthProviderEnabledRequest]) (*connect.Response[leapmuxv1.SetOAuthProviderEnabledResponse], error) {
	if err := s.requireOAuthProvider(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	if err := s.store.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
		Enabled: req.Msg.GetEnabled(),
		ID:      req.Msg.GetId(),
	}); err != nil {
		return nil, storeConnectError(err, "update provider")
	}
	// Unconditional, for both directions. A re-enable then rebuilds once on
	// the next request, and one rule reads better than two.
	s.cache.InvalidateProvider(req.Msg.GetId())
	return connect.NewResponse(&leapmuxv1.SetOAuthProviderEnabledResponse{}), nil
}
