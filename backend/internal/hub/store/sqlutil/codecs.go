package sqlutil

import (
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// SealSetting encrypts hub_settings value and secret columns with their
// canonical key-bound AAD.
func SealSetting(ks *keystore.Keystore, p store.UpsertSettingParams) (valCt, secCt keystore.NullCiphertext, err error) {
	valCt, err = ks.SealOptionalString(p.Value, SettingValueAAD(p.Key))
	if err != nil {
		return keystore.NullCiphertext{}, keystore.NullCiphertext{}, err
	}
	secCt, err = ks.SealNonEmptyBytes(p.Secret, SettingSecretAAD(p.Key))
	if err != nil {
		return keystore.NullCiphertext{}, keystore.NullCiphertext{}, err
	}
	return valCt, secCt, nil
}

// SealedOAuthProvider holds the encrypted columns of an oauth_providers row.
type SealedOAuthProvider struct {
	Name         keystore.Ciphertext
	IssuerURL    keystore.Ciphertext
	ClientID     keystore.Ciphertext
	ClientSecret keystore.Ciphertext
	Scopes       keystore.Ciphertext
}

// SealOAuthProvider encrypts all five protected columns of an oauth_providers
// row with their canonical provider-ID-bound AAD.
func SealOAuthProvider(ks *keystore.Keystore, p store.CreateOAuthProviderParams) (SealedOAuthProvider, error) {
	name, err := ks.SealString(p.Name, OAuthProviderNameAAD(p.ID))
	if err != nil {
		return SealedOAuthProvider{}, err
	}
	issuerURL, err := ks.SealString(p.IssuerURL, OAuthProviderIssuerURLAAD(p.ID))
	if err != nil {
		return SealedOAuthProvider{}, err
	}
	clientID, err := ks.SealString(p.ClientID, OAuthProviderClientIDAAD(p.ID))
	if err != nil {
		return SealedOAuthProvider{}, err
	}
	clientSecret, err := ks.SealBytes(p.ClientSecret, OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return SealedOAuthProvider{}, err
	}
	scopes, err := ks.SealString(p.Scopes, OAuthProviderScopesAAD(p.ID))
	if err != nil {
		return SealedOAuthProvider{}, err
	}
	return SealedOAuthProvider{
		Name:         name,
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
	}, nil
}

// OpenedOAuthProviderSummary holds the four decrypted summary fields of an
// oauth_providers row.
type OpenedOAuthProviderSummary struct {
	Name      string
	IssuerURL string
	ClientID  string
	Scopes    string
}

// OpenOAuthProviderSummary decrypts the four non-secret summary columns of an
// oauth_providers row.
func OpenOAuthProviderSummary(
	ks *keystore.Keystore,
	id string,
	nameCt, issuerURLCt, clientIDCt, scopesCt keystore.Ciphertext,
) (OpenedOAuthProviderSummary, error) {
	name, err := ks.OpenString(nameCt, OAuthProviderNameAAD(id))
	if err != nil {
		return OpenedOAuthProviderSummary{}, err
	}
	issuerURL, err := ks.OpenString(issuerURLCt, OAuthProviderIssuerURLAAD(id))
	if err != nil {
		return OpenedOAuthProviderSummary{}, err
	}
	clientID, err := ks.OpenString(clientIDCt, OAuthProviderClientIDAAD(id))
	if err != nil {
		return OpenedOAuthProviderSummary{}, err
	}
	scopes, err := ks.OpenString(scopesCt, OAuthProviderScopesAAD(id))
	if err != nil {
		return OpenedOAuthProviderSummary{}, err
	}
	return OpenedOAuthProviderSummary{
		Name:      name,
		IssuerURL: issuerURL,
		ClientID:  clientID,
		Scopes:    scopes,
	}, nil
}

// SealedOAuthClientFields holds the five non-icon encrypted metadata columns
// of an oauth_clients row.
type SealedOAuthClientFields struct {
	ClientName   keystore.Ciphertext
	ClientURI    keystore.Ciphertext
	RedirectURIs keystore.Ciphertext
	Scopes       keystore.Ciphertext
	GrantTypes   keystore.Ciphertext
}

// SealOAuthClientFields encrypts the five metadata columns of an oauth_clients
// row with their canonical client-ID-bound AAD.
func SealOAuthClientFields(
	ks *keystore.Keystore,
	clientID, clientName, clientURI, redirectURIs, scopes, grantTypes string,
) (SealedOAuthClientFields, error) {
	nameCt, err := ks.SealString(clientName, OAuthClientNameAAD(clientID))
	if err != nil {
		return SealedOAuthClientFields{}, err
	}
	uriCt, err := ks.SealString(clientURI, OAuthClientURIAAD(clientID))
	if err != nil {
		return SealedOAuthClientFields{}, err
	}
	redirectCt, err := ks.SealString(redirectURIs, OAuthClientRedirectURIsAAD(clientID))
	if err != nil {
		return SealedOAuthClientFields{}, err
	}
	scopesCt, err := ks.SealString(scopes, OAuthClientScopesAAD(clientID))
	if err != nil {
		return SealedOAuthClientFields{}, err
	}
	grantTypesCt, err := ks.SealString(grantTypes, OAuthClientGrantTypesAAD(clientID))
	if err != nil {
		return SealedOAuthClientFields{}, err
	}
	return SealedOAuthClientFields{
		ClientName:   nameCt,
		ClientURI:    uriCt,
		RedirectURIs: redirectCt,
		Scopes:       scopesCt,
		GrantTypes:   grantTypesCt,
	}, nil
}

// OpenedOAuthClientFields holds the five decrypted metadata fields of an
// oauth_clients row.
type OpenedOAuthClientFields struct {
	ClientName   string
	ClientURI    string
	RedirectURIs string
	Scopes       string
	GrantTypes   string
}

// OpenOAuthClientFields decrypts the five metadata columns of an oauth_clients
// row with their canonical client-ID-bound AAD.
func OpenOAuthClientFields(
	ks *keystore.Keystore,
	clientID string,
	nameCt, uriCt, redirectURIsCt, scopesCt, grantTypesCt keystore.Ciphertext,
) (OpenedOAuthClientFields, error) {
	clientName, err := ks.OpenString(nameCt, OAuthClientNameAAD(clientID))
	if err != nil {
		return OpenedOAuthClientFields{}, err
	}
	clientURI, err := ks.OpenString(uriCt, OAuthClientURIAAD(clientID))
	if err != nil {
		return OpenedOAuthClientFields{}, err
	}
	redirectURIs, err := ks.OpenString(redirectURIsCt, OAuthClientRedirectURIsAAD(clientID))
	if err != nil {
		return OpenedOAuthClientFields{}, err
	}
	scopes, err := ks.OpenString(scopesCt, OAuthClientScopesAAD(clientID))
	if err != nil {
		return OpenedOAuthClientFields{}, err
	}
	grantTypes, err := ks.OpenString(grantTypesCt, OAuthClientGrantTypesAAD(clientID))
	if err != nil {
		return OpenedOAuthClientFields{}, err
	}
	return OpenedOAuthClientFields{
		ClientName:   clientName,
		ClientURI:    clientURI,
		RedirectURIs: redirectURIs,
		Scopes:       scopes,
		GrantTypes:   grantTypes,
	}, nil
}

// SealOAuthClientIcon encrypts icon_blob and icon_media_type for an
// oauth_clients row, mapping empty values to SQL NULL so HasIcon tracks icon
// presence accurately.
func SealOAuthClientIcon(ks *keystore.Keystore, clientID string, iconBlob []byte, iconMediaType string) (blobCt, mediaTypeCt keystore.NullCiphertext, err error) {
	blobCt, err = ks.SealNonEmptyBytes(iconBlob, OAuthClientIconBlobAAD(clientID))
	if err != nil {
		return keystore.NullCiphertext{}, keystore.NullCiphertext{}, err
	}
	mediaTypeCt, err = ks.SealNonEmptyString(iconMediaType, OAuthClientIconMediaTypeAAD(clientID))
	if err != nil {
		return keystore.NullCiphertext{}, keystore.NullCiphertext{}, err
	}
	return blobCt, mediaTypeCt, nil
}

// OpenOAuthClientIcon decrypts icon_blob and icon_media_type for an
// oauth_clients row.
func OpenOAuthClientIcon(ks *keystore.Keystore, clientID string, blobCt, mediaTypeCt keystore.NullCiphertext) (iconBlob []byte, iconMediaType string, err error) {
	iconBlob, err = ks.OpenOptionalBytes(blobCt, OAuthClientIconBlobAAD(clientID))
	if err != nil {
		return nil, "", err
	}
	iconMediaType, err = ks.OpenStringOrDefault(mediaTypeCt, OAuthClientIconMediaTypeAAD(clientID), "")
	if err != nil {
		return nil, "", err
	}
	return iconBlob, iconMediaType, nil
}

// JoinedClientCache memoizes decrypted (client_name, scopes) pairs by
// client_id within a single API token listing query so tokens sharing one
// OAuth client do not decrypt the same client columns repeatedly.
type JoinedClientCache struct {
	ks    *keystore.Keystore
	items map[string]joinedClientPlain
}

type joinedClientPlain struct {
	name   string
	scopes string
}

// NewJoinedClientCache returns a per-query memoization cache backed by ks.
func NewJoinedClientCache(ks *keystore.Keystore) *JoinedClientCache {
	return &JoinedClientCache{
		ks:    ks,
		items: make(map[string]joinedClientPlain),
	}
}

// Open returns the decrypted (client_name, scopes) for clientID, decrypting
// each distinct clientID at most once per cache instance.
func (c *JoinedClientCache) Open(clientID string, nameCt, scopesCt keystore.Ciphertext) (name, scopes string, err error) {
	if cached, ok := c.items[clientID]; ok {
		return cached.name, cached.scopes, nil
	}
	name, err = c.ks.OpenString(nameCt, OAuthClientNameAAD(clientID))
	if err != nil {
		return "", "", err
	}
	scopes, err = c.ks.OpenString(scopesCt, OAuthClientScopesAAD(clientID))
	if err != nil {
		return "", "", err
	}
	c.items[clientID] = joinedClientPlain{name: name, scopes: scopes}
	return name, scopes, nil
}
