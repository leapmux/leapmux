package webauthn

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

func (s *Service) encryptSessionData(sessionID string, data *gowebauthn.SessionData) ([]byte, error) {
	if s.ks == nil {
		return nil, fmt.Errorf("keystore required for webauthn sessions")
	}
	plain, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal session data: %w", err)
	}
	return s.ks.Encrypt(plain, keystore.WebAuthnSessionDataAAD(sessionID))
}

func (s *Service) decryptSessionData(sessionID string, ciphertext []byte) (*gowebauthn.SessionData, error) {
	if s.ks == nil {
		return nil, fmt.Errorf("keystore required for webauthn sessions")
	}
	plain, err := s.ks.Decrypt(ciphertext, keystore.WebAuthnSessionDataAAD(sessionID))
	if err != nil {
		return nil, fmt.Errorf("decrypt session data: %w", err)
	}
	var data gowebauthn.SessionData
	if err := json.Unmarshal(plain, &data); err != nil {
		return nil, fmt.Errorf("unmarshal session data: %w", err)
	}
	return &data, nil
}

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

func (s *Service) encryptPayloadJSON(sessionID, plain string) (string, error) {
	if s.ks == nil {
		return "", fmt.Errorf("keystore required for webauthn payload")
	}
	enc, err := s.ks.Encrypt([]byte(plain), keystore.WebAuthnPayloadAAD(sessionID))
	if err != nil {
		return "", fmt.Errorf("encrypt signup draft: %w", err)
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func (s *Service) decryptPayloadJSON(sessionID, stored string) (string, error) {
	if s.ks == nil {
		return "", fmt.Errorf("keystore required for webauthn payload")
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", fmt.Errorf("decode signup draft: %w", err)
	}
	plain, err := s.ks.Decrypt(raw, keystore.WebAuthnPayloadAAD(sessionID))
	if err != nil {
		return "", fmt.Errorf("decrypt signup draft: %w", err)
	}
	return string(plain), nil
}
