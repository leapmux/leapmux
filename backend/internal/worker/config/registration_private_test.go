package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

type registrationRecordReader struct {
	name string
	data []byte
	path func(*Config) string
	read func(*Config) (bool, error)
}

func registrationRecordReaders(t *testing.T) []registrationRecordReader {
	t.Helper()
	var keys KeyMaterial
	_, err := keys.GenerateKeypair()
	require.NoError(t, err)
	state, err := json.Marshal(State{HubURL: "https://hub.example", WorkerID: "worker", AuthToken: "token", KeyMaterial: keys})
	require.NoError(t, err)
	pending, err := json.Marshal(PendingRegistration{AttemptID: "aabbccddaabbccddaabbccddaabbccdd", HubURL: "https://hub.example", RegKey: "key", KeyMaterial: keys})
	require.NoError(t, err)
	return []registrationRecordReader{
		{name: "state", data: state, path: (*Config).StatePath, read: func(cfg *Config) (bool, error) {
			value, err := cfg.LoadState()
			return value != nil, err
		}},
		{name: "pending", data: pending, path: (*Config).PendingPath, read: func(cfg *Config) (bool, error) {
			value, err := cfg.LoadPendingRegistration()
			return value != nil, err
		}},
	}
}

func TestPrivateRegistrationRecordReaders(t *testing.T) {
	const recordLimit = 16 << 20
	for _, reader := range registrationRecordReaders(t) {
		for _, kind := range []string{"missing", "valid", "empty", "malformed", "directory", "linked", "public", "size maximum", "oversized"} {
			t.Run(reader.name+"/"+kind, func(t *testing.T) {
				cfg := &Config{DataDir: t.TempDir()}
				path := reader.path(cfg)
				data := reader.data
				switch kind {
				case "missing":
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				case "linked":
					target := filepath.Join(cfg.DataDir, "private-target")
					require.NoError(t, atomicfile.WriteFileDurable(target, data, 0o600))
					require.NoError(t, os.Symlink(target, path))
				default:
					switch kind {
					case "empty":
						data = nil
					case "malformed":
						data = []byte("{invalid")
					case "size maximum":
						data = append(bytes.Clone(data), bytes.Repeat([]byte{' '}, recordLimit-len(data))...)
					case "oversized":
						data = append(bytes.Clone(data), bytes.Repeat([]byte{' '}, recordLimit+1-len(data))...)
					}
					require.NoError(t, atomicfile.WriteFileDurable(path, data, 0o600))
					if kind == "public" {
						makeRegistrationRecordPublic(t, path)
					}
				}
				present, err := reader.read(cfg)
				switch kind {
				case "missing":
					require.NoError(t, err)
					assert.False(t, present)
				case "valid", "size maximum":
					require.NoError(t, err)
					assert.True(t, present)
				default:
					require.Error(t, err)
				}
			})
		}
	}
}
