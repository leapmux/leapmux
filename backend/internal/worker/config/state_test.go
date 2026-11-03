package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// SaveState stamps the pid as the FIRST field and names this process. The pid
// is how a reader tells a live credentials file from a crashed run's, and the
// first position is what the reader sees before anything else.
func TestSaveState_PIDIsFirstAndNamesThisProcess(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	in := &State{WorkerID: "w1", AuthToken: "tok"}

	require.NoError(t, cfg.SaveState(in))

	raw, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^\{\s*"pid"`), string(raw),
		"pid must be the first JSON key")
	assert.Equal(t, os.Getpid(), in.PID, "the writer names this process")

	var got State
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, os.Getpid(), got.PID)
}

// The credentials file keeps mode 0600 through the atomic replace: the auth
// token and the private keys inside it must not be readable by anyone else.
func TestSaveState_ModeIs0600(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	require.NoError(t, cfg.SaveState(&State{WorkerID: "w1", AuthToken: "tok"}))

	info, err := os.Stat(cfg.StatePath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(testutil.PrivateFilePerm()), info.Mode().Perm())
}

// A successful SaveState leaves no temporary file behind, and a failed one
// cleans up the artifact it made. The temp holds the same secrets as the
// destination, so a residue is a leaked credentials file under a name no
// listing of the destination shows.
func TestSaveState_LeavesNoTemporaryFileBehind(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	require.NoError(t, cfg.SaveState(&State{WorkerID: "w1", AuthToken: "tok"}))

	entries, err := os.ReadDir(cfg.DataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a successful write must leave only the state file")
	assert.Equal(t, "state.json", entries[0].Name())

	// A destination the rename cannot replace: the write fails after the temp
	// is complete, and the temp must still not survive.
	require.NoError(t, os.Remove(cfg.StatePath()))
	require.NoError(t, os.Mkdir(cfg.StatePath(), 0o700))
	err = cfg.SaveState(&State{WorkerID: "w2", AuthToken: "tok2"})
	require.Error(t, err, "the rename onto a directory must fail")

	entries, err = os.ReadDir(cfg.DataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a failed write must not leave its temporary file behind")
	assert.Equal(t, "state.json", entries[0].Name())
}

// A round trip keeps every field, secrets included, so the atomic write cannot
// silently start dropping one.
func TestSaveState_RoundTripsEveryField(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	in := &State{
		WorkerID: "w1", AuthToken: "tok",
		KeyMaterial: KeyMaterial{
			X25519PrivateKey: "sk", MlkemPrivateKey: "msk", SlhdsaPrivateKey: "ssk",
		},
	}

	require.NoError(t, cfg.SaveState(in))
	got, err := cfg.LoadState()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, in, got)
}

// The saved file holds exactly the private halves, one top-level key each: the
// public halves used to ride along as a second copy, and a bare `public_key`
// beside a prefixed `x25519_private_key` would be that drift again.
func TestSaveState_PersistsOnlyThePrivateHalves(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	// Generated without a token, as runWorker does before it registers, then
	// saved with the credentials it received.
	in := &State{WorkerID: "w1"}
	_, _, err := in.EnsureCompositeKeypair()
	require.NoError(t, err)
	in.AuthToken = "tok"

	require.NoError(t, cfg.SaveState(in))

	raw, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t,
		[]string{"pid", "worker_id", "auth_token", "x25519_private_key", "mlkem_private_key", "slhdsa_private_key"},
		keys)
}

// EnsureCompositeKeypair generates for an unregistered state, keeps a usable
// keypair, regenerates a broken one when nothing is pinned, and refuses either
// failure once a token is present.
func TestState_EnsureCompositeKeypair(t *testing.T) {
	t.Run("generates for an unregistered state", func(t *testing.T) {
		state := &State{}
		ck, generated, err := state.EnsureCompositeKeypair()
		require.NoError(t, err)
		require.True(t, generated)
		require.NotNil(t, ck)
		assert.True(t, state.HasKeypair())

		restored, err := state.CompositeKeypair()
		require.NoError(t, err)
		assert.Equal(t, ck.Fingerprint(), restored.Fingerprint())
	})

	t.Run("keeps an existing keypair", func(t *testing.T) {
		state := &State{}
		first, _, err := state.EnsureCompositeKeypair()
		require.NoError(t, err)
		saved := state.KeyMaterial

		second, generated, err := state.EnsureCompositeKeypair()
		require.NoError(t, err)
		assert.False(t, generated)
		assert.Equal(t, first.Fingerprint(), second.Fingerprint())
		assert.Equal(t, saved, state.KeyMaterial, "an existing keypair is not rewritten")
	})

	t.Run("regenerates a broken keypair when unregistered", func(t *testing.T) {
		state := &State{KeyMaterial: KeyMaterial{
			X25519PrivateKey: "not!valid!base64",
			MlkemPrivateKey:  "also-not-base64",
			SlhdsaPrivateKey: "nope",
		}}
		ck, generated, err := state.EnsureCompositeKeypair()
		require.NoError(t, err, "nothing is pinned yet, so a broken keypair is replaced")
		assert.True(t, generated)
		require.NotNil(t, ck)
		assert.NotEqual(t, "not!valid!base64", state.X25519PrivateKey)
	})

	t.Run("refuses a token with no keypair", func(t *testing.T) {
		state := &State{AuthToken: "tok"}
		ck, generated, err := state.EnsureCompositeKeypair()
		require.ErrorIs(t, err, ErrUnusableRegisteredKeypair)
		assert.Nil(t, ck)
		assert.False(t, generated)
		assert.False(t, state.HasKeypair(), "the refusal leaves the fields unchanged")
	})

	// HasKeypair requires ALL three halves. A state that holds only some of them
	// is the boundary that tells "all" from "any": the cases above hold all three
	// or none.
	partialKeyMaterial := func(t *testing.T) KeyMaterial {
		t.Helper()
		var material KeyMaterial
		_, err := material.GenerateKeypair()
		require.NoError(t, err)
		material.SlhdsaPrivateKey = ""
		return material
	}

	t.Run("refuses a token with a partial keypair", func(t *testing.T) {
		material := partialKeyMaterial(t)
		state := &State{KeyMaterial: material, AuthToken: "tok"}
		assert.False(t, state.HasKeypair(), "one missing half makes the keypair missing")

		ck, generated, err := state.EnsureCompositeKeypair()
		require.ErrorIs(t, err, ErrUnusableRegisteredKeypair)
		assert.Nil(t, ck)
		assert.False(t, generated)
		assert.Equal(t, material, state.KeyMaterial, "the refusal leaves the fields unchanged")
	})

	t.Run("regenerates a partial keypair when unregistered", func(t *testing.T) {
		material := partialKeyMaterial(t)
		state := &State{KeyMaterial: material}

		ck, generated, err := state.EnsureCompositeKeypair()
		require.NoError(t, err, "nothing is pinned yet, so a partial keypair is replaced")
		assert.True(t, generated)
		require.NotNil(t, ck)
		assert.True(t, state.HasKeypair(), "all three halves are written")
		assert.NotEqual(t, material.X25519PrivateKey, state.X25519PrivateKey,
			"the halves that were present are replaced too, so the set stays one keypair")
	})

	t.Run("refuses a token with a broken keypair", func(t *testing.T) {
		state := &State{KeyMaterial: KeyMaterial{
			X25519PrivateKey: "not!valid!base64",
			MlkemPrivateKey:  "also-not-base64",
			SlhdsaPrivateKey: "nope",
		}, AuthToken: "tok"}
		ck, generated, err := state.EnsureCompositeKeypair()
		require.ErrorIs(t, err, ErrUnusableRegisteredKeypair)
		assert.Nil(t, ck)
		assert.False(t, generated)
		assert.Equal(t, "not!valid!base64", state.X25519PrivateKey, "the refusal leaves the fields unchanged")
	})
}

// decode64 states the field that failed to decode, so an operator can tell
// which key is mangled.
func TestDecode64NamesTheFieldThatFailed(t *testing.T) {
	_, err := decode64("mlkem_private_key", "not!valid!base64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mlkem_private_key")
}

// A mangled base64 field must fail the restore, not decode to nothing. The
// worker would otherwise connect with an intact public key (derived from the
// bytes it can still decode) and an EMPTY X25519 private key, so every channel
// handshake fails at the first DH -- a worker that looks online and serves
// nothing.
func TestState_CompositeKeypairRefusesAnUndecodableKey(t *testing.T) {
	state := &State{}
	_, _, err := state.EnsureCompositeKeypair()
	require.NoError(t, err)
	_, err = state.CompositeKeypair()
	require.NoError(t, err, "the generated keypair must restore before the field is mangled")

	state.X25519PrivateKey = "not!valid!base64"

	ck, err := state.CompositeKeypair()
	require.Error(t, err, "an undecodable private key must not restore as an empty one")
	assert.Nil(t, ck)
	assert.Contains(t, err.Error(), "x25519_private_key", "the error names the field")
}

// KeyMaterial round-trips through its base64 fields.
func TestKeyMaterial_CompositeKeypair(t *testing.T) {
	var km KeyMaterial
	generated, err := km.GenerateKeypair()
	require.NoError(t, err)

	restored, err := km.CompositeKeypair()
	require.NoError(t, err)
	assert.Equal(t, generated.Fingerprint(), restored.Fingerprint())
}

// The temp name is derived from the destination, and a cleanup sweep that only
// knew the old bare-write shape would miss it. This pins the sweep target a
// deleter must use.
func TestSaveState_TempNameIsSweepable(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir()}
	// A leftover from an interrupted write, under the name atomicfile uses.
	leftover := cfg.StatePath() + ".tmp12345"
	require.NoError(t, os.WriteFile(leftover, []byte(`{"pid":0}`), 0o600))

	matches, err := filepath.Glob(cfg.StatePath() + ".tmp*")
	require.NoError(t, err)
	assert.Equal(t, []string{leftover}, matches,
		"the residue name must be discoverable from the destination alone")
}
