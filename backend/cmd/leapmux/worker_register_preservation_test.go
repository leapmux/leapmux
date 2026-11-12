package main

import (
	"encoding/json"
	"os"
	"testing"

	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/worker/config"
)

func TestWorkerRegisterPreservesIncompleteSavedCredentials(t *testing.T) {
	complete := config.State{HubURL: "https://original-hub.example", WorkerID: "original-worker", AuthToken: "original-token"}
	_, err := complete.GenerateKeypair()
	require.NoError(t, err)
	for _, field := range []string{"hub_url", "worker_id", "auth_token", "empty object", "null"} {
		for _, pendingKind := range []string{"absent", "matching keys", "conflicting keys", "malformed"} {
			t.Run(field+"/"+pendingKind, func(t *testing.T) {
				env := startRegisterEnv(t)
				cfg := workerConfigOf(t, t.TempDir())
				var fields map[string]json.RawMessage
				encoded, marshalErr := json.Marshal(complete)
				require.NoError(t, marshalErr)
				require.NoError(t, json.Unmarshal(encoded, &fields))
				delete(fields, field)
				encoded, marshalErr = json.Marshal(fields)
				require.NoError(t, marshalErr)
				switch field {
				case "empty object":
					encoded = []byte("{}")
				case "null":
					encoded = []byte("null")
				}
				files := map[string][]byte{
					cfg.StatePath():            encoded,
					cfg.StatePath() + ".tmp-a": []byte("saved interrupted credential copy"),
					cfg.DBPath():               []byte("existing worker database"),
				}
				if pendingKind != "absent" {
					pending := config.PendingRegistration{AttemptID: "aabbccddaabbccddaabbccddaabbccdd", HubURL: env.url, RegKey: env.key, KeyMaterial: complete.KeyMaterial}
					if pendingKind == "conflicting keys" {
						_, generateErr := pending.GenerateKeypair()
						require.NoError(t, generateErr)
					}
					pendingBytes, marshalErr := json.Marshal(pending)
					require.NoError(t, marshalErr)
					if pendingKind == "malformed" {
						pendingBytes = []byte("{unfinished")
					}
					files[cfg.PendingPath()] = pendingBytes
					files[cfg.PendingPath()+".tmp-a"] = []byte("saved interrupted pending copy")
				}
				for path, content := range files {
					require.NoError(t, atomicfile.WriteFileDurable(path, content, 0o600))
				}

				registerErr := runWorkerRegister([]string{"--hub", env.url, "--registration-key", env.key, "--data-dir", cfg.DataDir})
				require.ErrorContains(t, registerErr, "saved registration is incomplete")
				assert.Zero(t, env.registerCalls.Load(), "partial credentials must be refused before networking")
				for path, expected := range files {
					actual, readErr := os.ReadFile(path)
					require.NoError(t, readErr)
					assert.Equal(t, expected, actual, path)
				}
				if pendingKind == "absent" {
					assert.NoFileExists(t, cfg.PendingPath())
				}
				_, keyErr := env.store.RegistrationKeys().GetByKeyHash(t.Context(), hubtestutil.TestSecretHasher().Hash(env.key))
				require.NoError(t, keyErr, "the unused registration key must remain usable")
			})
		}
	}
}
