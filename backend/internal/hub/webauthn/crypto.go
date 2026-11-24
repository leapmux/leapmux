package webauthn

import (
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

func (s *Service) encryptPublicKey(credentialRowID string, publicKey []byte) ([]byte, int64, error) {
	if s.ks == nil {
		return nil, 0, fmt.Errorf("keystore required for passkey credentials")
	}
	enc, err := s.ks.Encrypt(publicKey, keystore.PasskeyPublicKeyAAD(credentialRowID))
	if err != nil {
		return nil, 0, err
	}
	return enc, int64(s.ks.ActiveVersion()), nil
}

func (s *Service) decryptPublicKey(credentialRowID string, ciphertext []byte) ([]byte, error) {
	if s.ks == nil {
		return nil, fmt.Errorf("keystore required for passkey credentials")
	}
	return s.ks.Decrypt(ciphertext, keystore.PasskeyPublicKeyAAD(credentialRowID))
}
