package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type acpFSWireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func readACPFSWireResponse(t *testing.T, stdin *agenttest.Stdin, id string) acpFSWireResponse {
	t.Helper()
	var reply []byte
	require.Eventually(t, func() bool {
		reply = []byte(stdin.String())
		return len(reply) > 0
	}, 30*time.Second, 10*time.Millisecond, "the filesystem request must receive a reply")
	var response acpFSWireResponse
	require.NoError(t, json.Unmarshal(reply, &response))
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, id, string(response.ID))
	return response
}

func requestACPFSWireResponse(t *testing.T, method, id string, params any) acpFSWireResponse {
	t.Helper()
	encoded, err := json.Marshal(params)
	require.NoError(t, err)
	request := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":` + string(encoded) + `}`)
	stdin := &agenttest.Stdin{}
	base, _ := newACPTurnBase(t, stdin)
	base.handleACPOutput(providerkit.ParseLine(request))
	return readACPFSWireResponse(t, stdin, id)
}

func TestACPFSReadMissingFileReturnsResourceNotFound(t *testing.T) {
	t.Parallel()

	for _, id := range []string{`0`, `9007199254740993`, `"missing-file"`} {
		t.Run(id, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.txt")
			response := requestACPFSWireResponse(t, acpMethodFSReadTextFile, id, acpFSReadTextFileParams{
				SessionID: "session-1",
				Path:      path,
			})
			assert.Empty(t, response.Result)
			require.NotNil(t, response.Error)
			assert.Equal(t, -32002, response.Error.Code)
			assert.Equal(t, "Resource not found: "+path, response.Error.Message)
		})
	}
}

func TestACPFSWireReadWritePreservesExactContentAndIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []string{`0`, `-1`, `9007199254740993`, `"file-\"id"`} {
		for index, content := range []string{"", "before\n\t\"quoted\"\x00", string(make([]byte, 131072))} {
			t.Run(fmt.Sprintf("%s/content-%d", id, index), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "note.txt")
				write := requestACPFSWireResponse(t, acpMethodFSWriteTextFile, id, acpFSWriteTextFileParams{
					SessionID: "session-1", Path: path, Content: content,
				})
				assert.Nil(t, write.Error)
				assert.JSONEq(t, `{}`, string(write.Result))
				bytes, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, content, string(bytes))
				read := requestACPFSWireResponse(t, acpMethodFSReadTextFile, id, acpFSReadTextFileParams{
					SessionID: "session-1", Path: path,
				})
				assert.Nil(t, read.Error)
				var result struct {
					Content *string `json:"content"`
				}
				require.NoError(t, json.Unmarshal(read.Result, &result))
				require.NotNil(t, result.Content)
				assert.Equal(t, content, *result.Content)
			})
		}
	}
}

func TestACPFSWireRejectsInvalidParameters(t *testing.T) {
	t.Parallel()
	for _, method := range []string{acpMethodFSReadTextFile, acpMethodFSWriteTextFile} {
		for _, params := range []any{nil, map[string]any{}, map[string]any{"path": ""}, map[string]any{"path": 0}, false, []any{}} {
			t.Run(fmt.Sprintf("%s/%v", method, params), func(t *testing.T) {
				response := requestACPFSWireResponse(t, method, `0`, params)
				assert.Empty(t, response.Result)
				require.NotNil(t, response.Error)
				assert.Equal(t, -32602, response.Error.Code)
				assert.Contains(t, response.Error.Message, "params")
				if params == nil {
					assert.Equal(t, "Invalid params: a path is required", response.Error.Message)
				}
			})
		}
	}
	t.Run("write rejects invalid content before changing the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unchanged.txt")
		require.NoError(t, os.WriteFile(path, []byte("before"), 0o600))
		response := requestACPFSWireResponse(t, acpMethodFSWriteTextFile, `0`, map[string]any{"path": path, "content": false})
		require.NotNil(t, response.Error)
		assert.Equal(t, -32602, response.Error.Code)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "before", string(content))
	})
}

func TestACPFSWriteMissingParentReturnsResourceNotFound(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent", "note.txt")
	response := requestACPFSWireResponse(t, acpMethodFSWriteTextFile, `9007199254740993`, acpFSWriteTextFileParams{
		SessionID: "session-1", Path: path, Content: "not written",
	})
	assert.Empty(t, response.Result)
	require.NotNil(t, response.Error)
	assert.Equal(t, -32002, response.Error.Code)
	assert.Equal(t, "Resource not found: "+path, response.Error.Message)
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestACPFSWirePreservesUnrelatedFilesystemErrors(t *testing.T) {
	t.Parallel()
	for _, method := range []string{acpMethodFSReadTextFile, acpMethodFSWriteTextFile} {
		t.Run(method, func(t *testing.T) {
			path := t.TempDir()
			var operationErr error
			if method == acpMethodFSReadTextFile {
				_, operationErr = fsReadTextFile(path)
			} else {
				operationErr = fsWriteTextFile(path, "not written")
			}
			require.Error(t, operationErr)
			require.NotErrorIs(t, operationErr, os.ErrNotExist)
			response := requestACPFSWireResponse(t, method, `"directory"`, map[string]any{"path": path, "content": "not written"})
			assert.Empty(t, response.Result)
			require.NotNil(t, response.Error)
			assert.Equal(t, -32603, response.Error.Code)
			assert.Equal(t, method+": "+operationErr.Error(), response.Error.Message)
		})
	}
}

func TestACPFSWireDetachedRepliesKeepEachRequestID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("the actual file\n"), 0o600))
	params, err := json.Marshal(acpFSReadTextFileParams{SessionID: "session-1", Path: path})
	require.NoError(t, err)
	stdin := &agenttest.Stdin{}
	base, _ := newACPTurnBase(t, stdin)
	ids := []string{`0`, `-1`, `9007199254740993`, `"concurrent"`}
	for _, id := range ids {
		request := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"fs/read_text_file","params":` + string(params) + `}`)
		base.handleACPOutput(providerkit.ParseLine(request))
	}
	var replies []string
	require.Eventually(t, func() bool {
		replies = strings.Split(strings.TrimSpace(stdin.String()), "\n")
		return len(replies) == len(ids)
	}, 30*time.Second, 10*time.Millisecond)
	seen := make(map[string]bool)
	for _, reply := range replies {
		var response acpFSWireResponse
		require.NoError(t, json.Unmarshal([]byte(reply), &response))
		assert.Equal(t, "2.0", response.JSONRPC)
		assert.Nil(t, response.Error)
		assert.JSONEq(t, `{"content":"the actual file\n"}`, string(response.Result))
		assert.False(t, seen[string(response.ID)], "each request must receive one reply")
		seen[string(response.ID)] = true
	}
	for _, id := range ids {
		assert.True(t, seen[id], "the detached writer must preserve the exact request ID")
	}
}

func TestACPFSWireMissingIDDoesNotChangeTheFile(t *testing.T) {
	t.Parallel()
	for _, id := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		path := filepath.Join(t.TempDir(), "absent.txt")
		params, err := json.Marshal(acpFSWriteTextFileParams{SessionID: "session-1", Path: path, Content: "not written"})
		require.NoError(t, err)
		stdin := &agenttest.Stdin{}
		base, _ := newACPTurnBase(t, stdin)
		base.handleFSMethod(&providerkit.ParsedLine{ID: id, Method: acpMethodFSWriteTextFile, Params: params})
		assert.Empty(t, stdin.String())
		_, err = os.Stat(path)
		assert.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestACPFSWireSuccessRetainsTheLateToolArguments(t *testing.T) {
	t.Parallel()
	for _, method := range []string{acpMethodFSReadTextFile, acpMethodFSWriteTextFile} {
		t.Run(method, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.txt")
			require.NoError(t, os.WriteFile(path, []byte("before"), 0o600))
			stdin := &agenttest.Stdin{}
			base, sink := newACPTurnBase(t, stdin)
			base.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"file-tool","title":"Read or write the file","kind":"read","status":"pending"}`))
			params, err := json.Marshal(acpFSWriteTextFileParams{SessionID: "session-1", Path: path, Content: ""})
			require.NoError(t, err)
			base.handleFSMethod(&providerkit.ParsedLine{ID: json.RawMessage(`0`), Method: method, Params: params})
			response := readACPFSWireResponse(t, stdin, `0`)
			assert.Nil(t, response.Error)
			messages := sink.Messages()
			require.Len(t, messages, 1)
			var supplement map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &supplement))
			var input map[string]any
			require.NoError(t, json.Unmarshal(supplement[contracts.ACPSupplementRequestRawInput], &input))
			want := map[string]any{"path": path}
			if method == acpMethodFSWriteTextFile {
				want["content"] = ""
			}
			assert.Equal(t, want, input)
		})
	}
}

// The filesystem host preserves file text and rejects an absent path.
// Dirac's edit_file requires these read and write operations.
func TestFSReadWriteTextFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("before\n"), 0o644))

	content, err := fsReadTextFile(path)
	require.NoError(t, err)
	assert.Equal(t, "before\n", content)

	require.NoError(t, fsWriteTextFile(path, "after\n"))
	written, err := fsReadTextFile(path)
	require.NoError(t, err)
	assert.Equal(t, "after\n", written)

	_, err = fsReadTextFile(filepath.Join(dir, "missing.txt"))
	assert.Error(t, err)
	assert.Error(t, fsWriteTextFile("", "x"))
	_, err = fsReadTextFile("")
	assert.Error(t, err)
}

// The filesystem host adds request arguments to a tool call that lacks input.
// The host request supplies the path after the runtime opens the call.
func TestLatestOpenToolWithoutInput_FlagsTheInputlessCall(t *testing.T) {
	t.Parallel()

	var out acpTurnOutput
	out.rememberIncompleteTool("with-input", map[string]json.RawMessage{
		"rawInput": json.RawMessage(`{"path":"a"}`),
	}, []byte(`{}`))
	assert.Equal(t, "", out.latestOpenToolWithoutInput(), "a call that states its input is not the one the fs request enriches")

	out.rememberIncompleteTool("without-input", map[string]json.RawMessage{
		"kind": json.RawMessage(`"read"`),
	}, []byte(`{}`))
	assert.Equal(t, "without-input", out.latestOpenToolWithoutInput())

	out.rememberIncompleteTool("later-without-input", map[string]json.RawMessage{
		"rawInput": json.RawMessage(`{}`),
	}, []byte(`{}`))
	assert.Equal(t, "later-without-input", out.latestOpenToolWithoutInput(),
		"an empty object states no input, and the most recently opened call wins")
}
