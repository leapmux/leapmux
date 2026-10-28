package fastagent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inputBlocks(t *testing.T, raw string) []json.RawMessage {
	t.Helper()
	var blocks []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &blocks))
	return blocks
}

func TestFastagentInputProofMatchesNativeContentConversion(t *testing.T) {
	cases := []struct{ name, dispatched, native string }{
		{"empty array", `[]`, `[]`},
		{"empty text", `[{"type":"text","text":""}]`, `[{"type":"text","text":""}]`},
		{"text and image", `[{"type":"text","text":"inspect"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png","uri":"image.png"}]`, `[{"type":"text","text":"inspect"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]`},
		{"pdf blob", `[{"type":"resource","resource":{"uri":"file:///a.pdf","mimeType":"application/pdf","blob":"cGRm"}}]`, `[{"type":"resource","resource":{"uri":"file:///a.pdf","mimeType":"application/pdf","blob":"cGRm"}}]`},
		{"current audio resource", `[{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"YXVkaW8="}}]`, `[{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"YXVkaW8="}}]`},
		{"acp audio", `[{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"}]`, `[{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"}]`},
		{"embedded text", `[{"type":"resource","resource":{"uri":"file:///note","text":""}}]`, `[{"type":"resource","resource":{"uri":"file:///note","text":""}}]`},
		{"empty mime omitted", `[{"type":"resource","resource":{"uri":"file:///note","mimeType":"","text":"native"}}]`, `[{"type":"resource","resource":{"uri":"file:///note","text":"native"}}]`},
		{"annotation priority zero", `[{"type":"text","text":"native","annotations":{"priority":0,"audience":["user"],"lastModified":"discarded","_meta":{"discarded":true}}}]`, `[{"type":"text","text":"native","annotations":{"priority":0.0,"audience":["user"]}}]`},
		{"negative zero", `[{"type":"text","text":"native","annotations":{"priority":-0.0}}]`, `[{"type":"text","text":"native","annotations":{"priority":0}}]`},
		{"empty annotations", `[{"type":"text","text":"native","annotations":{}}]`, `[{"type":"text","text":"native","annotations":{}}]`},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			dispatched := inputBlocks(t, scenario.dispatched)
			before := make([]json.RawMessage, len(dispatched))
			for i := range dispatched {
				before[i] = slices.Clone(dispatched[i])
			}
			want, err := fastagentInputDigest(dispatched, true)
			require.NoError(t, err)
			got, err := fastagentInputDigest(inputBlocks(t, scenario.native), false)
			require.NoError(t, err)
			assert.Equal(t, want, got)
			assert.Equal(t, before, dispatched, "proof preparation does not rewrite the dispatched blocks")
		})
	}
}

func TestFastagentInputProofRefusesChangedOrUnrepresentedInput(t *testing.T) {
	original := `[{"type":"text","text":"inspect"},{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"YXVkaW8="}}]`
	want, err := fastagentInputDigest(inputBlocks(t, original), true)
	require.NoError(t, err)
	for _, raw := range []string{
		`[{"type":"text","text":"changed"},{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"YXVkaW8="}}]`,
		`[{"type":"text","text":"inspect"}]`,
		`[{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"YXVkaW8="}},{"type":"text","text":"inspect"}]`,
		`[{"type":"text","text":"inspect"},{"type":"resource","resource":{"uri":"file:///other.wav","mimeType":"audio/wav","blob":"YXVkaW8="}}]`,
		`[{"type":"text","text":"inspect"},{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/mpeg","blob":"YXVkaW8="}}]`,
		`[{"type":"text","text":"inspect"},{"type":"resource","resource":{"uri":"file:///a.wav","mimeType":"audio/wav","blob":"b3RoZXI="}}]`,
	} {
		got, err := fastagentInputDigest(inputBlocks(t, raw), false)
		require.NoError(t, err)
		assert.NotEqual(t, want, got)
	}
	empty, err := fastagentInputDigest(inputBlocks(t, `[]`), true)
	require.NoError(t, err)
	text, err := fastagentInputDigest(inputBlocks(t, `[{"type":"text","text":""}]`), false)
	require.NoError(t, err)
	assert.NotEqual(t, empty, text)
	_, err = fastagentInputDigest(nil, true)
	require.Error(t, err, "null is not an explicit empty block array")
}

func TestFastagentInputProofRejectsAmbiguousNativeAndUnsupportedACPBlocks(t *testing.T) {
	for _, raw := range []string{
		`[null]`, `[{"type":"text"}]`, `[{"type":"text","text":0}]`,
		`[{"type":"image","data":"x"}]`, `[{"type":"future","text":"x"}]`,
		`[{"type":"resource","resource":{"uri":"file:///x","text":"x","blob":"x"}}]`,
		`[{"type":"resource","resource":{"uri":"file:///x"}}]`,
		`[{"type":"text","text":"x","annotations":{"priority":-1}}]`,
		`[{"type":"text","text":"x","annotations":{"priority":2}}]`,
		`[{"type":"text","text":"x","annotations":{"audience":["system"]}}]`,
		`[{"type":"text","text":"x","_meta":{"unexpected":true}}]`,
		`[{"type":"resource","resource":{"uri":"file:///x","text":"x","unexpected":true}}]`,
	} {
		_, err := fastagentInputDigest(inputBlocks(t, raw), false)
		require.Error(t, err)
	}
	_, err := fastagentInputDigest(inputBlocks(t, `[{"type":"text","text":"/command"},{"type":"resource","resource":{"uri":"file:///x","text":"x"}}]`), true)
	require.Error(t, err)
}
