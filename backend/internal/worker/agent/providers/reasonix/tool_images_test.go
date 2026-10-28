package reasonix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadReasonixImageBlobVerifiesThePayloadAndReadLimit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	image := "data:image/png;base64,iVBORw0KGgo="
	digest := sha256.Sum256([]byte(image))
	hash := hex.EncodeToString(digest[:])
	path := filepath.Join(directory, hash)
	require.NoError(t, os.WriteFile(path, []byte(image), 0o600))
	assert.Equal(t, image, readReasonixImageBlob(directory, "sha256:"+hash, len(image)))
	assert.Equal(t, image, readReasonixImageBlob(directory, "sha256:"+hash, math.MaxInt))
	for _, limit := range []int{-1, 0, len(image) - 1} {
		assert.Empty(t, readReasonixImageBlob(directory, "sha256:"+hash, limit))
	}
	for _, key := range []string{"", hash, "sha256:../private", "sha256:" + strings.ToUpper(hash), "sha256:" + strings.Repeat("0", 64)} {
		assert.Empty(t, readReasonixImageBlob(directory, key, len(image)))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", len(image))), 0o600))
	assert.Empty(t, readReasonixImageBlob(directory, "sha256:"+hash, len(image)), "a valid filename cannot authenticate corrupted bytes")
}

func TestRestoreReasonixRecordImagesKeepsMessageOwnership(t *testing.T) {
	t.Parallel()
	text := json.RawMessage(`{"role":"tool","tool_call_id":"call","content":"native text","custom":true}`)
	blobs := json.RawMessage(`[{"message":0,"slots":[{"url":"data:image/png;base64,AQID"}]},{"message":1,"slots":[{"url":"data:image/png;base64,BAUG"}]}]`)
	for index, expected := range []string{"data:image/png;base64,AQID", "data:image/png;base64,BAUG"} {
		restored := restoreReasonixRecordImages("unused.events.jsonl", text, blobs, index)
		var message map[string]any
		require.NoError(t, json.Unmarshal(restored, &message))
		assert.Equal(t, "native text", message["content"])
		assert.Equal(t, true, message["custom"])
		assert.Equal(t, []any{expected}, message["images"])
	}
}

func TestRestoreReasonixRecordImagesKeepsTextWhenImagesAreUnavailable(t *testing.T) {
	t.Parallel()
	text := json.RawMessage(`{"role":"tool","tool_call_id":"call","content":"native text"}`)
	for _, blobs := range []string{
		`null`, `{broken`, `[{"message":0,"slots":42}]`,
		`[{"slots":[{"url":"data:image/png;base64,AQID"}]}]`,
		`[{"message":-1,"slots":[{"url":"data:image/png;base64,AQID"}]}]`,
		`[{"message":1,"slots":[{"url":"data:image/png;base64,AQID"}]}]`,
		`[{"message":0,"slots":[]}]`,
		`[{"message":0,"slots":[{"blob":"sha256:../private"}]}]`,
		`[{"message":0,"slots":[{"blob":"sha256:missing","url":"data:image/png;base64,AQID"}]}]`,
		`[{"message":0,"slots":[{"url":"first"}]},{"message":0,"slots":[{"url":"second"}]}]`,
	} {
		assert.Equal(t, text, restoreReasonixRecordImages("unused.events.jsonl", text, json.RawMessage(blobs), 0), blobs)
	}
}

func TestReasonixToolStoreReadsMixedNativeRecordVersions(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"schema_version": 1, "type": "replace", "messages": []any{reasonixFixtureRecord("first")}},
		map[string]any{"schema_version": 3, "type": "append", "message_index": 1, "messages": []any{reasonixFixtureRecord("image version")}},
		map[string]any{"schema_version": 4, "type": "append", "message_index": 2, "messages": []any{reasonixFixtureRecord("composition version")}},
		map[string]any{"schema_version": 5, "type": "append", "message_index": 3, "messages": []any{reasonixFixtureRecord("live composition version")}},
	)
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
	require.NoError(t, err)
	assert.Contains(t, string(records["call"]), "live composition version")
}
