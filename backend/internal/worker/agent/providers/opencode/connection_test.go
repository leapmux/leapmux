package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeServiceRegistrationRequiresTheExactPrivateOwner(t *testing.T) {
	t.Parallel()
	base := openCodeServiceRegistration{ID: "instance", Version: openCodeNativeVersion, URL: "http://127.0.0.1:49374", PID: 42, Password: "private-credential"}
	for _, tc := range []struct {
		name   string
		change func(*openCodeServiceRegistration)
	}{
		{name: "missing ID", change: func(r *openCodeServiceRegistration) { r.ID = "" }},
		{name: "zero PID", change: func(r *openCodeServiceRegistration) { r.PID = 0 }},
		{name: "negative PID", change: func(r *openCodeServiceRegistration) { r.PID = -1 }},
		{name: "missing credential", change: func(r *openCodeServiceRegistration) { r.Password = "" }},
		{name: "wrong version", change: func(r *openCodeServiceRegistration) { r.Version = "1.19.4" }},
		{name: "relative address", change: func(r *openCodeServiceRegistration) { r.URL = "/relative" }},
		{name: "embedded credential", change: func(r *openCodeServiceRegistration) { r.URL = "http://private-credential@127.0.0.1:49374" }},
		{name: "query", change: func(r *openCodeServiceRegistration) { r.URL += "?password=private-credential" }},
		{name: "fragment", change: func(r *openCodeServiceRegistration) { r.URL += "#private-credential" }},
		{name: "path", change: func(r *openCodeServiceRegistration) { r.URL += "/private-credential" }},
		{name: "zero port", change: func(r *openCodeServiceRegistration) { r.URL = "http://127.0.0.1:0" }},
		{name: "large port", change: func(r *openCodeServiceRegistration) { r.URL = "http://127.0.0.1:65536" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registration := base
			tc.change(&registration)
			raw, err := json.Marshal(registration)
			require.NoError(t, err)
			_, err = decodeOpenCodeServiceRegistration(raw)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-credential")
		})
	}
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	actual, err := decodeOpenCodeServiceRegistration(raw)
	require.NoError(t, err)
	assert.Equal(t, base, actual)
	for _, raw := range []string{"", "null", "[]", "{}", "{", string(raw) + " {}", `{"password":"private-credential",`} {
		_, err := decodeOpenCodeServiceRegistration([]byte(raw))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private-credential")
	}
}

func TestOpenCodeServiceOwnerComparisonIncludesCredentialAndGeneration(t *testing.T) {
	t.Parallel()
	original := openCodeServiceRegistration{ID: "generation-a", Version: openCodeNativeVersion, URL: "http://127.0.0.1:49374", PID: 42, Password: "secret"}
	assert.True(t, original.sameOwner(original))
	for _, change := range []func(*openCodeServiceRegistration){
		func(r *openCodeServiceRegistration) { r.ID = "generation-b" }, func(r *openCodeServiceRegistration) { r.Version = "other" }, func(r *openCodeServiceRegistration) { r.PID++ }, func(r *openCodeServiceRegistration) { r.URL = strings.ReplaceAll(r.URL, "49374", "49375") }, func(r *openCodeServiceRegistration) { r.Password = "replacement" },
	} {
		replacement := original
		change(&replacement)
		assert.False(t, original.sameOwner(replacement))
	}
}
