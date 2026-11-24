package hub

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestNewServerRejectsMissingOrWrongEncryptionKeyWithoutMutatingDB(t *testing.T) {
	ctx := context.Background()

	seedHubDBWithoutCaptcha := func(t *testing.T, dataDir string) (*config.Config, *keystore.Keystore) {
		t.Helper()
		cfg := &config.Config{
			DataDir: dataDir,
			Listen:  []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks")},
			Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
		}
		ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
		require.NoError(t, err)
		st, err := storeopen.Open(ctx, cfg, ks)
		require.NoError(t, err)
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID:           "admin-1",
			Username:     "admin",
			PasswordHash: "hashed-password",
			DisplayName:  "Admin",
			IsAdmin:      true,
		}))
		require.NoError(t, st.Close())
		return cfg, ks
	}

	assertBuiltInsReadable := func(t *testing.T, cfg *config.Config, origKS *keystore.Keystore) {
		t.Helper()
		st, err := storeopen.OpenWithoutMigrate(ctx, cfg, origKS)
		require.NoError(t, err)
		defer func() { _ = st.Close() }()
		for _, app := range oauthapp.BuiltInRegistrations() {
			got, getErr := st.OAuthClients().Get(ctx, app.ClientID)
			require.NoError(t, getErr, "built-in %s must remain decryptable under original keystore", app.ClientID)
			assert.Equal(t, app.ClientName, got.ClientName)
		}
	}

	t.Run("hub mode without captcha row refuses missing key file without generating one", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, origKS := seedHubDBWithoutCaptcha(t, dataDir)
		keyPath := cfg.EncryptionKeyFilePath()
		require.NoError(t, os.Remove(keyPath))

		_, err := NewServer(cfg, testHubLease(t, dataDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already contains encrypted data")

		_, statErr := os.Stat(keyPath)
		assert.ErrorIs(t, statErr, os.ErrNotExist, "missing encryption.key must not be regenerated against an existing DB")
		assertBuiltInsReadable(t, cfg, origKS)
	})

	t.Run("hub mode without captcha row refuses wrong key without overwriting built-ins", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, origKS := seedHubDBWithoutCaptcha(t, dataDir)
		keyPath := cfg.EncryptionKeyFilePath()
		require.NoError(t, os.Remove(keyPath))
		// Generate a different version-1 key in place.
		_, err := keystore.LoadOrGenerate(keyPath)
		require.NoError(t, err)

		_, err = NewServer(cfg, testHubLease(t, dataDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verify keyring")
		assertBuiltInsReadable(t, cfg, origKS)
	})

	stopServer := func(t *testing.T, srv *Server) {
		t.Helper()
		serveCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- srv.Serve(serveCtx) }()
		<-srv.Ready()
		cancel()
		require.NoError(t, <-done)
	}

	t.Run("hub mode after full boot refuses wrong key without overwriting built-ins", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := &config.Config{
			DataDir: dataDir,
			Listen:  []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks-full")},
			Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
		}
		lease1 := testHubLease(t, dataDir)
		srv, err := NewServer(cfg, lease1)
		require.NoError(t, err)
		stopServer(t, srv)

		keyPath := cfg.EncryptionKeyFilePath()
		origKS, err := keystore.LoadFromFile(keyPath)
		require.NoError(t, err)
		require.NoError(t, os.Remove(keyPath))
		_, err = keystore.LoadOrGenerate(keyPath)
		require.NoError(t, err)

		cfg.Listen = []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks-full-2")}
		_, err = NewServer(cfg, testHubLease(t, dataDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verify keyring")
		assertBuiltInsReadable(t, cfg, origKS)
	})

	t.Run("solo mode refuses missing and wrong key without generating or overwriting built-ins", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := &config.Config{
			DataDir:  dataDir,
			Listen:   []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks-solo")},
			Storage:  config.StorageConfig{Type: config.StorageTypeSQLite},
			SoloMode: true,
		}
		srv, err := NewServer(cfg, testHubLease(t, dataDir))
		require.NoError(t, err)
		stopServer(t, srv)

		keyPath := cfg.EncryptionKeyFilePath()
		origKS, err := keystore.LoadFromFile(keyPath)
		require.NoError(t, err)

		// Missing key file must fail and stay absent.
		require.NoError(t, os.Remove(keyPath))
		cfg.Listen = []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks-solo-2")}
		_, err = NewServer(cfg, testHubLease(t, dataDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already contains encrypted data")
		_, statErr := os.Stat(keyPath)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
		assertBuiltInsReadable(t, cfg, origKS)

		// Replaced wrong key must fail without overwriting built-ins.
		_, err = keystore.LoadOrGenerate(keyPath)
		require.NoError(t, err)
		cfg.Listen = []string{locallistentest.UniqueListenURL(t, "lmx-hub-ks-solo-3")}
		_, err = NewServer(cfg, testHubLease(t, dataDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verify keyring")
		assertBuiltInsReadable(t, cfg, origKS)
	})
}
