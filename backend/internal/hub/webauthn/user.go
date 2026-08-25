package webauthn

import (
	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// user adapts a LeapMux account to go-webauthn's User interface. source
// carries the loaded store row so callers can reuse it without a second
// query after a ceremony.
type user struct {
	id          []byte
	name        string
	displayName string
	credentials []gowebauthn.Credential
	source      *store.User
}

func (u *user) WebAuthnID() []byte                           { return u.id }
func (u *user) WebAuthnName() string                         { return u.name }
func (u *user) WebAuthnDisplayName() string                  { return u.displayName }
func (u *user) WebAuthnCredentials() []gowebauthn.Credential { return u.credentials }

func userIDBytes(userID string) []byte {
	return []byte(userID)
}
