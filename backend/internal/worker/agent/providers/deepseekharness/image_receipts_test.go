package deepseekharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageReceiptOmitsEveryCompleteTextAndSubprocessBody(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	f := prepareNativeImageObserverFixture(t)
	const hidden = "NATIVE_EXTERNAL_TEXT_MUST_NOT_ENTER_WORKER_7841"
	var imageData bytes.Buffer
	require.NoError(t, png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	sum := sha256.Sum256(imageData.Bytes())
	ref := nativeImageReference{ID: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "image/png", Bytes: int64(imageData.Len()), Width: 2, Height: 3}
	manifest := map[string]any{"config": f.config, "attachment": ref, "data": base64.StdEncoding.EncodeToString(imageData.Bytes()), "hidden": hidden, "spill": filepath.Join(f.temporary, "dsh-subprocess-native", "dsh-subprocess-42-1-0123456789ab-stdout.log")}
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest["spill"].(string)), 0o700))
	require.NoError(t, os.WriteFile(manifest["spill"].(string), []byte(hidden), 0o600))
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestPath := filepath.Join(f.directory, "observer-input.json")
	require.NoError(t, os.WriteFile(manifestPath, raw, 0o600))
	script := `
import fs from 'node:fs';
import {syncBuiltinESMExports} from 'node:module';
import {pathToFileURL} from 'node:url';
const [program,input] = process.argv.slice(1);
const m = JSON.parse(fs.readFileSync(input,'utf8'));
const originalRead = fs.readFileSync;
let spillReads = 0;
fs.readFileSync = (path,...args) => { if (path === m.spill) spillReads++; return originalRead(path,...args); };
syncBuiltinESMExports();
const handlers = new Map();
const ctx = {
 on(event,handler) { handlers.set(event,handler); },
 get(name) {
  if (name === 'fs') return {processPathFromHostPath(path) {return path;}};
  if (name === 'attachments') return {
   imageHostPath() {return '/native/image.png';},
   async readImage() {return {ref:m.attachment,data:Buffer.from(m.data,'base64')};}
  };
 },
 logger:{warn() {}}
};
const {apply} = await import(pathToFileURL(program).href);
apply(ctx,m.config);
const exec = {token:Symbol(), agent:{session:{id:'native-session'}}, callId:'native-call', name:'bash'};
const before = {isError:false,content:[{type:'text',text:m.hidden},{type:'image',attachment:m.attachment}],value:{stdout:{text:'native preview',truncated:true,spillPath:m.spill}}};
const after = {isError:false,content:[{type:'text',text:'native preview'}],value:{stdout:{text:'native preview',truncated:true,spillPath:m.spill}}};
await handlers.get('tools/post-execute')(exec,before,async()=>({kind:'accept',content:after.content}));
handlers.get('tools/result')(exec,after);
handlers.get('dispose')();
const [leaf] = fs.readdirSync(m.config.receiptDirectory);
const receipt = JSON.parse(originalRead(m.config.receiptDirectory+'/'+leaf,'utf8'));
process.stdout.write(JSON.stringify({spillReads,receipt}));
`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, f.program, manifestPath).CombinedOutput()
	require.NoError(t, err, string(output))
	var observed struct {
		SpillReads int                        `json:"spillReads"`
		Receipt    map[string]json.RawMessage `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal(output, &observed))
	assert.Zero(t, observed.SpillReads, "the official observer must not read subprocess output bodies")
	for _, field := range []string{"before", "after", "content", "value", "formatted", "files", "text", "stdout", "stderr"} {
		assert.NotContains(t, observed.Receipt, field)
	}
	assert.NotContains(t, string(output), hidden)
	assert.Contains(t, string(output), ref.ID)
}
func TestImageReceiptKeepsOmittedNativeImagesWithoutTextRecovery(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	f := prepareNativeImageObserverFixture(t)
	installNativeImageObserverFixture(t, a, f)
	var pixels bytes.Buffer
	require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	imageSum := sha256.Sum256(pixels.Bytes())
	id := "sha256:" + hex.EncodeToString(imageSum[:])
	ref := nativeImageReference{ID: id, MediaType: "image/png", Bytes: int64(pixels.Len()), Width: 2, Height: 3}
	const callID = "native-call"
	const toolName = "mcp__results__inspect"
	sessionID := a.sessionID
	sessionSum, callSum := sha256.Sum256([]byte(sessionID)), sha256.Sum256([]byte(callID))
	receipt := map[string]any{"sessionId": sessionID, "callId": callID, "toolName": toolName, "isError": false, "originalImages": []any{map[string]any{"position": 1, "attachment": ref}}, "retainedImages": []any{}, "images": []any{map[string]any{"attachment": ref, "data": base64.StdEncoding.EncodeToString(pixels.Bytes())}}}
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.receipts, hex.EncodeToString(sessionSum[:])+"."+hex.EncodeToString(callSum[:])+".json"), raw, 0o600))
	opening, err := json.Marshal(map[string]any{"type": "tool/call", "seq": 1, "time": 1, "data": map[string]any{"callId": callID, "name": toolName, "arguments": "{}"}})
	require.NoError(t, err)
	original, err := json.Marshal(map[string]any{"type": "tool/result", "seq": 2, "time": 2, "data": map[string]any{"message": map[string]any{"toolCallId": callID, "isError": false, "content": []any{map[string]any{"type": "text", "text": "native preview\n\n(Omitted 20 bytes. Omitted 1 images. Full formatted result stored at: /native/never-open-this.txt. Use read with offset/limit, or grep this path to search within it.)"}}}}})
	require.NoError(t, err)
	require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: opening, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName}))
	require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName, Closing: true}))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, original, messages[1].Content)
	assert.Contains(t, string(messages[1].SupplementalContent), id, "the exact image-only receipt must keep omitted images without formatted text")
	assert.NotContains(t, string(messages[1].SupplementalContent), `"outputFile"`)
	assert.NotContains(t, string(messages[1].SupplementalContent), `"text"`)
}

func TestReceiptImagesRefuseForeignMissingDuplicateAndChangedBytes(t *testing.T) {
	t.Parallel()
	var pixels bytes.Buffer
	require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	sum := sha256.Sum256(pixels.Bytes())
	ref := nativeImageReference{ID: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "image/png", Bytes: int64(pixels.Len()), Width: 2, Height: 3}
	for _, tc := range []struct {
		name   string
		change func(*imageReceipt)
	}{
		{name: "missing bytes", change: func(receipt *imageReceipt) { receipt.Images = nil }},
		{name: "duplicate reference", change: func(receipt *imageReceipt) { receipt.Images = append(receipt.Images, receipt.Images[0]) }},
		{name: "foreign native reference", change: func(receipt *imageReceipt) { receipt.Images[0].Attachment.ID = "sha256:" + strings.Repeat("a", 64) }},
		{name: "different dimensions", change: func(receipt *imageReceipt) { receipt.Images[0].Attachment.Width = 99 }},
		{name: "malformed encoding", change: func(receipt *imageReceipt) { receipt.Images[0].Data = "invalid base64" }},
		{name: "changed bytes", change: func(receipt *imageReceipt) {
			receipt.Images[0].Data = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, pixels.Len()))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var receipt imageReceipt
			raw, err := json.Marshal(map[string]any{"sessionId": "native-session", "callId": "native-call", "toolName": "native-tool", "isError": false, "originalImages": []any{map[string]any{"position": 0, "attachment": ref}}, "retainedImages": []any{}, "images": []any{map[string]any{"attachment": ref, "data": base64.StdEncoding.EncodeToString(pixels.Bytes())}}})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &receipt))
			tc.change(&receipt)
			supplement, err := receiptImageSupplement(receipt, "native-session", "native-call", 1<<20)
			require.Error(t, err)
			assert.Empty(t, supplement)
		})
	}
}

func TestImageReceiptKeySupportsIndependentUnicodeIdentities(t *testing.T) {
	t.Parallel()
	assert.NotEqual(t, imageReceiptLeaf("session:a", "b"), imageReceiptLeaf("session", "a:b"))
	assert.NotEqual(t, imageReceiptLeaf("session", "a"), imageReceiptLeaf("session", "b"))
	assert.NotEqual(t, imageReceiptLeaf("s1", "a"), imageReceiptLeaf("s2", "a"))
	assert.NotEqual(t, imageReceiptLeaf("한😀", "a"), imageReceiptLeaf("한", "😀a"))
}

func TestImageReceiptValidatesNativeIdentityPositionsAndLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*imageReceipt)
		limit  int
	}{
		{name: "foreign session", change: func(r *imageReceipt) { r.SessionID = "foreign" }},
		{name: "foreign call", change: func(r *imageReceipt) { r.CallID = "foreign" }},
		{name: "missing error flag", change: func(r *imageReceipt) { r.IsError = nil }},
		{name: "missing retained list", change: func(r *imageReceipt) { r.RetainedImages = nil }},
		{name: "missing original images", change: func(r *imageReceipt) { r.OriginalImages = nil }},
		{name: "missing position", change: func(r *imageReceipt) { r.OriginalImages[0].Position = nil }},
		{name: "negative position", change: func(r *imageReceipt) { value := -1; r.OriginalImages[0].Position = &value }},
		{name: "duplicate position", change: func(r *imageReceipt) { r.OriginalImages = append(r.OriginalImages, r.OriginalImages[0]) }},
		{name: "foreign retained image", change: func(r *imageReceipt) {
			value := r.OriginalImages[0]
			value.Attachment.ID = "foreign"
			*r.RetainedImages = []imagePosition{value}
		}},
		{name: "unsupported media", change: func(r *imageReceipt) { r.OriginalImages[0].Attachment.MediaType = "text/plain" }},
		{name: "zero bytes", change: func(r *imageReceipt) { r.OriginalImages[0].Attachment.Bytes = 0 }},
		{name: "negative height", change: func(r *imageReceipt) { r.OriginalImages[0].Attachment.Height = -1 }},
		{name: "zero limit", limit: -1},
		{name: "negative limit", limit: -2},
		{name: "encoded limit", limit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var pixels bytes.Buffer
			require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
			sum := sha256.Sum256(pixels.Bytes())
			ref := nativeImageReference{ID: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "image/png", Bytes: int64(pixels.Len()), Width: 2, Height: 3}
			position := 0
			errorFlag := false
			retained := []imagePosition{}
			receipt := imageReceipt{SessionID: "native-session", CallID: "native-call", ToolName: "native-tool", IsError: &errorFlag, OriginalImages: []imagePosition{{Position: &position, Attachment: ref}}, RetainedImages: &retained, Images: []imageValue{{Attachment: ref, Data: base64.StdEncoding.EncodeToString(pixels.Bytes())}}}
			if tc.change != nil {
				tc.change(&receipt)
			}
			maximum := 1 << 20
			if tc.limit != 0 {
				maximum = tc.limit
				if maximum == -1 {
					maximum = 0
				}
			}
			extra, err := receiptImageSupplement(receipt, "native-session", "native-call", maximum)
			require.Error(t, err)
			assert.Empty(t, extra)
		})
	}
}

// Source: `@deepseek-ai/dsh` 0.2.0-rc.2. `read_image` saves the file with `name: basename(path)`
// (dsh-tool-fs), and a reference of dsh-attachment may state `originalDimensions` when its
// normalization reduced the image. Both are optional fields of the native reference, and the
// native image hook copies the whole reference into its receipt. The five fields that identify
// the image stay exact. The other fields of the native object must not refuse the receipt.
func TestImageReceiptAcceptsTheOptionalFieldsOfANativeImageReference(t *testing.T) {
	t.Parallel()
	for _, retained := range []bool{false, true} {
		name := "an omitted image"
		if retained {
			name = "a retained image"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			f := prepareNativeImageObserverFixture(t)
			installNativeImageObserverFixture(t, a, f)
			var pixels bytes.Buffer
			require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
			imageSum := sha256.Sum256(pixels.Bytes())
			id := "sha256:" + hex.EncodeToString(imageSum[:])
			ref := map[string]any{
				"attachmentId": id, "mediaType": "image/png", "bytes": pixels.Len(), "width": 2, "height": 3,
				"name": "tool-image-native.png", "originalDimensions": map[string]any{"width": 4, "height": 6},
			}
			const callID = "native-call"
			const toolName = "read_image"
			sessionID := a.sessionID
			sessionSum, callSum := sha256.Sum256([]byte(sessionID)), sha256.Sum256([]byte(callID))
			retainedImages := []any{}
			content := []any{map[string]any{"type": "text", "text": "native preview"}}
			if retained {
				retainedImages = []any{map[string]any{"position": 0, "attachment": ref}}
				content = []any{map[string]any{"type": "image", "attachment": ref}, map[string]any{"type": "text", "text": "native preview"}}
			}
			position := 1
			if retained {
				position = 0
			}
			receipt := map[string]any{"sessionId": sessionID, "callId": callID, "toolName": toolName, "isError": false, "originalImages": []any{map[string]any{"position": position, "attachment": ref}}, "retainedImages": retainedImages, "images": []any{map[string]any{"attachment": ref, "data": base64.StdEncoding.EncodeToString(pixels.Bytes())}}}
			raw, err := json.Marshal(receipt)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(f.receipts, hex.EncodeToString(sessionSum[:])+"."+hex.EncodeToString(callSum[:])+".json"), raw, 0o600))
			opening, err := json.Marshal(map[string]any{"type": "tool/call", "seq": 1, "time": 1, "data": map[string]any{"callId": callID, "name": toolName, "arguments": "{}"}})
			require.NoError(t, err)
			original, err := json.Marshal(map[string]any{"type": "tool/result", "seq": 2, "time": 2, "data": map[string]any{"message": map[string]any{"toolCallId": callID, "isError": false, "content": content}}})
			require.NoError(t, err)
			require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: opening, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName}))
			require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName, Closing: true}))
			messages := sink.Messages()
			require.Len(t, messages, 2)
			assert.Equal(t, original, messages[1].Content)
			assert.Contains(t, string(messages[1].SupplementalContent), id, "the receipt of a reference with optional native fields must supply its image")
		})
	}
}

func TestImageReceiptStillRefusesAnotherReferenceOfTheSameImage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	f := prepareNativeImageObserverFixture(t)
	installNativeImageObserverFixture(t, a, f)
	var pixels bytes.Buffer
	require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	imageSum := sha256.Sum256(pixels.Bytes())
	id := "sha256:" + hex.EncodeToString(imageSum[:])
	reference := func(width int) map[string]any {
		return map[string]any{"attachmentId": id, "mediaType": "image/png", "bytes": pixels.Len(), "width": width, "height": 3, "name": "tool-image-native.png"}
	}
	const callID = "native-call"
	const toolName = "read_image"
	sessionID := a.sessionID
	sessionSum, callSum := sha256.Sum256([]byte(sessionID)), sha256.Sum256([]byte(callID))
	// The saved value states another width than the original reference: an identity field differs.
	receipt := map[string]any{"sessionId": sessionID, "callId": callID, "toolName": toolName, "isError": false, "originalImages": []any{map[string]any{"position": 1, "attachment": reference(2)}}, "retainedImages": []any{}, "images": []any{map[string]any{"attachment": reference(5), "data": base64.StdEncoding.EncodeToString(pixels.Bytes())}}}
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.receipts, hex.EncodeToString(sessionSum[:])+"."+hex.EncodeToString(callSum[:])+".json"), raw, 0o600))
	opening, err := json.Marshal(map[string]any{"type": "tool/call", "seq": 1, "time": 1, "data": map[string]any{"callId": callID, "name": toolName, "arguments": "{}"}})
	require.NoError(t, err)
	original, err := json.Marshal(map[string]any{"type": "tool/result", "seq": 2, "time": 2, "data": map[string]any{"message": map[string]any{"toolCallId": callID, "isError": false, "content": []any{map[string]any{"type": "text", "text": "native preview"}}}}})
	require.NoError(t, err)
	require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: opening, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName}))
	require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, AgentSessionID: sessionID}, agent.SpanInfo{SpanID: callID, SpanType: toolName, Closing: true}))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.NotContains(t, string(messages[1].SupplementalContent), id, "a changed identity field refuses the receipt")
}
