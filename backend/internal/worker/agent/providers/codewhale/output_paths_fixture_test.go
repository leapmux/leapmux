package codewhale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type savedTextFixture struct {
	agent                                                        *Agent
	sink                                                         *agenttest.ControlSink
	home, path, sidecar, text, callID, toolName, nativeSessionID string
	metadata                                                     map[string]any
	evidence                                                     map[string]any
}

func nativeSavedTextFixture(t *testing.T, text string) *savedTextFixture {
	t.Helper()
	a, sink := newTestAgent(t, newFakeRuntime(t))
	home := t.TempDir()
	ctx, cancel := context.WithCancel(a.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "cw-1", ProviderName: codewhaleProviderName, Ctx: ctx, Cancel: cancel, Cmd: &exec.Cmd{Env: []string{"CODEWHALE_HOME=" + home, "HOME=" + t.TempDir()}}, ProcessDone: done, StderrDone: done})
	callID, toolName, nativeSessionID := "native-saved-mcp", "mcp_result_probe_inspect", "23601ee5-bd86-4058-a0e1-aec92016f3ce"
	outputFileID := "art_" + callID
	relative := "artifacts/" + outputFileID + ".txt"
	path := filepath.Join(home, "sessions", nativeSessionID, "artifacts", outputFileID+".txt")
	sidecar := filepath.Join(filepath.Dir(path), outputFileID+".evidence.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	digest := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(digest[:])
	now := a.clock.Now().UnixMilli()
	contentType := "text/plain"
	if json.Valid([]byte(text)) {
		contentType = "application/json"
	}
	evidence := map[string]any{"handle": outputFileID, "digest": hash, "size_bytes": len([]byte(text)), "content_type": contentType, "tool_name": toolName, "call_id": callID, "origin_session": nativeSessionID, "generation": 1, "redacted": false, "encoding": "utf-8", "retention_state": "live", "created_at_unix_ms": now - 1000, "retain_until_unix_ms": now + int64(time.Hour/time.Millisecond), "storage_path": relative}
	metadata := map[string]any{"artifact_id": outputFileID, "artifact_session_id": nativeSessionID, "spillover_path": path, "artifact_relative_path": relative, "artifact_byte_size": len([]byte(text)), "artifact_digest": hash, "artifact_generation": 1, "artifact_encoding": "utf-8", "artifact_retention_state": "live", "truncated": true, "evidence_available": true}
	f := &savedTextFixture{agent: a, sink: sink, home: home, path: path, sidecar: sidecar, text: text, callID: callID, toolName: toolName, nativeSessionID: nativeSessionID, metadata: metadata, evidence: evidence}
	f.writeEvidence(t)
	return f
}

func (f *savedTextFixture) writeEvidence(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.sidecar, data, 0o600))
}

func (f *savedTextFixture) close(t *testing.T) ([]byte, []byte) {
	t.Helper()
	input := map[string]any{"count": 0, "enabled": false, "text": ""}
	f.agent.HandleOutput(toolStartEvent(1, "saved-text-item", f.callID, f.toolName, input))
	closing := toolEndEvent(2, "item.completed", "saved-text-item", f.callID, f.toolName, "Native retained head\n[native omitted-output footer]\nNative retained tail", input, f.metadata)
	f.agent.HandleOutput(closing)
	rows := f.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, closing, rows[1].Content, "complete recovery must not rewrite the original closing event")
	return closing, rows[1].SupplementalContent
}
