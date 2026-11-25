package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnvelopeControlRequestKey(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    string
		ok      bool
	}{
		"the request_id as it stands": {`{"type":"control_request","request_id":" req 1 "}`, " req 1 ", true},
		"an empty request_id":         {`{"request_id":""}`, "", false},
		"a number":                    {`{"request_id":7}`, "", false},
		"null":                        {`{"request_id":null}`, "", false},
		"no request_id":               {`{"id":7}`, "", false},
		"no object":                   {`["request_id"]`, "", false},
		"no JSON":                     {`{"request_id":`, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := EnvelopeControlRequestKey([]byte(tc.payload))
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJSONRPCControlRequestIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    string
		ok      bool
	}{
		"a number":                  {`{"jsonrpc":"2.0","id":7,"method":"m"}`, "jsonrpc:7", true},
		"a number in another form":  {`{"id":7.0}`, "jsonrpc:7", true},
		"a string":                  {`{"id":"7"}`, `jsonrpc:"7"`, true},
		"null":                      {`{"id":null}`, "", false},
		"no id":                     {`{"method":"m"}`, "", false},
		"an object":                 {`{"id":{"a":1}}`, "", false},
		"a payload that is no JSON": {`{"id":`, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := JSONRPCControlRequestIdentity([]byte(tc.payload))
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got.Key)
		})
	}
}

// The publication of a JSON-RPC request and the key function derive one key
// from one identity, so a stored JSON-RPC request always agrees with its key.
func TestJSONRPCControlRequestIdentityIsTheKeyOfThePublication(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1e3,"method":"m"}`)
	identity, ok := JSONRPCControlRequestIdentity(payload)
	assert.True(t, ok)
	assert.Equal(t, "jsonrpc:1000", identity.Key)
	assert.JSONEq(t, `1e3`, string(identity.Native), "the answer echoes the id as the runtime spelled it")
	key, ok := ProviderDefaults{}.ControlRequestKey(payload, "")
	assert.True(t, ok)
	assert.Equal(t, identity.Key, key)
}

func TestProviderDefaults_ControlRequestKey(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    string
		ok      bool
	}{
		"an envelope":            {`{"type":"control_request","request_id":"req-1"}`, "req-1", true},
		"a JSON-RPC request":     {`{"jsonrpc":"2.0","id":7,"method":"m"}`, "jsonrpc:7", true},
		"an envelope with an id": {`{"request_id":"req-1","id":7}`, "req-1", true},
		// A payload that states a request_id is an envelope. When that
		// request_id gives no key, the id beside it gives none either.
		"an empty envelope key with an id": {`{"request_id":"","id":7}`, "", false},
		"a key that is no string":          {`{"request_id":7}`, "", false},
		"neither shape":                    {`{"type":"other"}`, "", false},
		"no object":                        {`[1]`, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := ProviderDefaults{}.ControlRequestKey([]byte(tc.payload), "session-1")
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// keyRecorder gives key for every payload that it reads, and records the
// session that it reads the key in.
type keyRecorder struct {
	Provider
	key     string
	ok      bool
	session string
	reads   int
}

func (r *keyRecorder) ControlRequestKey(_ []byte, agentSessionID string) (string, bool) {
	r.reads++
	r.session = agentSessionID
	return r.key, r.ok
}

func TestStoredControlRequestAgrees(t *testing.T) {
	t.Run("a payload that gives its key agrees", func(t *testing.T) {
		provider := &keyRecorder{key: "req-1", ok: true}
		assert.True(t, StoredControlRequestAgrees(provider, "req-1", []byte(`{}`), "session-1"))
		assert.Equal(t, "session-1", provider.session, "the provider reads the key in the request's session")
	})
	t.Run("a payload that gives another key disagrees", func(t *testing.T) {
		assert.False(t, StoredControlRequestAgrees(&keyRecorder{key: "req-2", ok: true}, "req-1", []byte(`{}`), ""))
	})
	t.Run("a payload that gives no key disagrees", func(t *testing.T) {
		assert.False(t, StoredControlRequestAgrees(&keyRecorder{key: "req-1", ok: false}, "req-1", []byte(`{}`), ""))
	})
	t.Run("an empty key agrees with nothing", func(t *testing.T) {
		provider := &keyRecorder{key: "", ok: true}
		assert.False(t, StoredControlRequestAgrees(provider, "", []byte(`{}`), ""))
		assert.Zero(t, provider.reads)
	})
	// A payload that is no JSON object reaches no provider.
	for _, payload := range []string{``, `not json`, `[1]`, `"req-1"`, `null`} {
		t.Run("the payload "+payload, func(t *testing.T) {
			provider := &keyRecorder{key: "req-1", ok: true}
			assert.False(t, StoredControlRequestAgrees(provider, "req-1", []byte(payload), ""))
			assert.Zero(t, provider.reads)
		})
	}
}
