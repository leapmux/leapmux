package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
)

func TestEnsureRequestID_UsesIDGenerateWhenOmitted(t *testing.T) {
	// Ensure auto-generated request IDs use LeapMux's uniform 48-char id.Generate()
	// alphabet rather than hyphenated UUIDs.
	inv := &cli.Invocation{Command: NewCommand()}
	got, explicit, err := ensureRequestID(inv)
	require.NoError(t, err)
	assert.False(t, explicit)
	assert.Len(t, got, id.GeneratedLength)
	assert.NotContains(t, got, "-", "auto-generated request_id should use id.Generate() alphanumeric alphabet")
}

func TestOutputSchema_CompileExtractAndValidate(t *testing.T) {
	schemaJSON := `{
		"type": "object",
		"properties": {
			"verdict": {"type": "string", "enum": ["pass", "fail"]},
			"count": {"type": "integer", "minimum": 0}
		},
		"required": ["verdict", "count"],
		"additionalProperties": false
	}`
	spec, err := compileOutputSchema([]byte(schemaJSON), 2)
	require.NoError(t, err)

	// Direct valid JSON passes.
	raw, err := extractAndValidateJSON(`{"verdict":"pass","count":3}`, spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"verdict":"pass","count":3}`, string(raw))

	// Fenced JSON block fallback passes.
	fenced := "Here is the output:\n```json\n{\"verdict\": \"fail\", \"count\": 0}\n```"
	raw, err = extractAndValidateJSON(fenced, spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"verdict":"fail","count":0}`, string(raw))

	// Schema violation returns a structured path error.
	_, err = extractAndValidateJSON(`{"verdict":"maybe","count":-1}`, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON Schema validation failed")

	// Empty or prose-only reply fails cleanly.
	_, err = extractAndValidateJSON("no json here", spec)
	require.Error(t, err)
	_, err = extractAndValidateJSON("   ", spec)
	require.Error(t, err)

	// Non-object schema is rejected at compile time.
	_, err = compileOutputSchema([]byte(`true`), 2)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestOutputSchema_FlagValidation(t *testing.T) {
	// --output-schema without --wait fails before dialing.
	o := runControl(t, "agent", "send", "ag-1", "hi", "--output-schema", `{"type":"object"}`)
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--output-schema needs --wait")

	// --schema-retries without --output-schema fails before dialing.
	o = runControl(t, "agent", "send", "ag-1", "hi", "--wait", "--schema-retries", "1")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--schema-retries needs --output-schema")

	// Invalid --schema-retries range fails.
	o = runControl(t, "agent", "send", "ag-1", "hi", "--wait", "--output-schema", `{"type":"object"}`, "--schema-retries", "99")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--schema-retries must be between 0 and 10")

	// --output-schema on agent open without --message/--file fails.
	o = runControl(t, "agent", "open", "--wait", "--output-schema", `{"type":"object"}`)
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--output-schema needs a prompt")

	// Schema loaded from file path compiles cleanly; invalid JSON in file fails.
	badFile := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(badFile, []byte("{not json"), 0o600))
	o = runControl(t, "agent", "send", "ag-1", "hi", "--wait", "--output-schema", badFile)
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "not valid JSON")
}

func TestAgentReplyResult_RenderTextPrefersStructuredJSON(t *testing.T) {
	res := agentReplyResult{
		ID:             "ag-1",
		InputID:        "in-1",
		Replies:        []string{"```json\n{\"ok\":true}\n```"},
		Structured:     json.RawMessage(`{"ok":true}`),
		SchemaAttempts: 2,
	}
	var buf bytes.Buffer
	require.NoError(t, res.RenderText(&buf))
	assert.Equal(t, "{\"ok\":true}\n", buf.String())
}

func TestTerminalWait_ChangeAndAfterOffset(t *testing.T) {
	// Flag validation checks.
	o := runControl(t, "terminal", "wait", "t-1", "--after-offset", "10")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--after-offset needs --change")

	o = runControl(t, "terminal", "wait", "t-1", "--change", "--exit")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "exclude each other")

	o = runControl(t, "terminal", "wait", "t-1", "--change", "--scrollback")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--scrollback needs --text or --regex")

	o = runControl(t, "terminal", "wait", "t-1", "--change", "--after-offset", "-1")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Stderr, "--after-offset must be 0 or greater")

	// Event-driven screen change detection ignores no-op escape sequences and
	// resolves immediately on visible screen diff.
	info := &leapmuxv1.TerminalInfo{
		TerminalId:      "t-1",
		Cols:            80,
		Rows:            24,
		Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
		Screen:          []byte("$ "),
		ScreenEndOffset: 2,
	}
	screen, err := newScreenWatch(info, nil, false)
	require.NoError(t, err)
	defer screen.dispose()
	initial, err := screen.snapshotVisibleText()
	require.NoError(t, err)
	assert.Equal(t, "$", initial)

	items := make(chan screenFeedItem, 4)
	// First chunk: OSC title change only (no visible text change).
	items <- screenFeedItem{
		kind: screenFeedEvent,
		event: &leapmuxv1.TerminalEvent{
			Event: &leapmuxv1.TerminalEvent_Data{
				Data: &leapmuxv1.TerminalData{
					Data:      []byte("\x1b]0;new title\x07"),
					EndOffset: 16,
				},
			},
		},
	}
	// Second chunk: visible output arrives.
	items <- screenFeedItem{
		kind: screenFeedEvent,
		event: &leapmuxv1.TerminalEvent{
			Event: &leapmuxv1.TerminalEvent_Data{
				Data: &leapmuxv1.TerminalData{
					Data:      []byte("hello\r\n"),
					EndOffset: 23,
				},
			},
		},
	}

	w := newTerminalWaiter("t-1", terminalWaitSpec{change: true, timeout: 5 * time.Second})
	res, outcome, err := awaitScreenChange(context.Background(), quartz.NewReal(), items, screen, w, initial)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.True(t, res.Reached)
	assert.True(t, res.Changed)
	assert.Equal(t, int64(23), res.ScreenEndOffset)
	assert.Equal(t, "$ hello", res.Screen)

	var textOut bytes.Buffer
	require.NoError(t, res.RenderText(&textOut))
	assert.Equal(t, "$ hello\n", textOut.String())
}

func TestTerminalWait_ChangeWithAfterOffsetImmediateReturn(t *testing.T) {
	// When --after-offset is strictly less than ScreenEndOffset, waitForScreenChange
	// returns immediately with Changed=true without opening any stream.
	info := &leapmuxv1.TerminalInfo{
		TerminalId:      "t-1",
		Cols:            80,
		Rows:            24,
		Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
		Screen:          []byte("ready\r\n"),
		ScreenEndOffset: 42,
	}
	spec := terminalWaitSpec{change: true, hasAfterOffset: true, afterOffset: 10, timeout: time.Second}
	res, outcome, err := waitForScreenChange(nil, tabTarget{ID: "t-1"}, info, spec)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.True(t, res.Reached)
	assert.True(t, res.Changed)
	assert.Equal(t, int64(42), res.ScreenEndOffset)
	assert.Equal(t, "ready", res.Screen)
}

func TestOutputSchema_AugmentAndCorrectionPrompt(t *testing.T) {
	spec, err := compileOutputSchema([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`), 2)
	require.NoError(t, err)
	aug := augmentPromptWithSchema("Check build", spec)
	assert.Contains(t, aug, "Check build")
	assert.Contains(t, aug, `"required":["ok"]`)

	_, valErr := extractAndValidateJSON(`{"ok":"nope"}`, spec)
	require.Error(t, valErr)
	corr := schemaCorrectionPrompt(valErr, spec)
	assert.Contains(t, corr, "JSON Schema validation failed")
	assert.Contains(t, corr, `"required":["ok"]`)
}
