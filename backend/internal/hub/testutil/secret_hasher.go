package testutil

import "github.com/leapmux/leapmux/internal/hub/auth"

// TestSecretHasher gives credential fixtures and their validators the same pepper.
func TestSecretHasher() auth.SecretHasher {
	hasher, err := auth.NewSecretHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	return hasher
}
