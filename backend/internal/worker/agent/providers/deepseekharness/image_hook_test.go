package deepseekharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageHookUsesPrivateFilesAndLiteralNativeConfiguration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	files, err := prepareImageHook(directory)
	require.NoError(t, err)
	require.NoError(t, agentDirSpec().Validate())
	for _, path := range []string{files.Receipts, files.Temporary} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Zero(t, info.Mode().Perm()&0o077)
	}
	program, err := os.ReadFile(filepath.Join(directory, "image_hook.mjs"))
	require.NoError(t, err)
	assert.Equal(t, imageHookProgram, program)
	raw, err := os.ReadFile(files.Overlay)
	require.NoError(t, err)
	var rows []struct {
		Insert []struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Config imageHookConfig `json:"config"`
		} `json:"insert"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Insert, 1)
	assert.Equal(t, "leapmux-image-receipts", rows[0].Insert[0].ID)
	assert.Equal(t, filepath.Join(directory, "image_hook.mjs"), rows[0].Insert[0].Name)
	assert.Equal(t, files.Receipts, rows[0].Insert[0].Config.ReceiptDirectory)
}

func TestImageHookRefusesAnUnsafeOrExistingDirectoryLayout(t *testing.T) {
	t.Parallel()
	_, err := prepareImageHook("relative")
	require.ErrorContains(t, err, "canonical absolute")
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = prepareImageHook(file)
	require.ErrorContains(t, err, "regular directory")
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(t.TempDir(), link))
	_, err = prepareImageHook(link)
	require.ErrorContains(t, err, "regular directory")
	prepared := t.TempDir()
	_, err = prepareImageHook(prepared)
	require.NoError(t, err)
	_, err = prepareImageHook(prepared)
	require.ErrorIs(t, err, os.ErrExist)
}

func TestImageHookStoresOneImageProofForRepeatedNativeOccurrences(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	require.NoError(t, err, "the native output hook test requires Node.js")
	directory := t.TempDir()
	files, err := prepareImageHook(directory)
	require.NoError(t, err)
	const sessionID = "native-session"
	const callID = "native-mixed-result"
	imageBytes := func(pixel color.RGBA) []byte {
		pixels := image.NewRGBA(image.Rect(0, 0, 64, 64))
		pixels.SetRGBA(0, 0, pixel)
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, pixels))
		return data.Bytes()
	}
	firstBytes := imageBytes(color.RGBA{R: 127, A: 255})
	secondBytes := imageBytes(color.RGBA{B: 127, A: 255})
	imageRef := func(data []byte) nativeImageReference {
		sum := sha256.Sum256(data)
		return nativeImageReference{ID: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "image/png", Bytes: int64(len(data)), Width: 64, Height: 64}
	}
	first, second := imageRef(firstBytes), imageRef(secondBytes)
	content := []any{
		map[string]string{"type": "text", "text": "head"},
		map[string]any{"type": "image", "attachment": first},
		map[string]string{"type": "text", "text": "middle"},
		map[string]any{"type": "image", "attachment": second},
		map[string]any{"type": "image", "attachment": first},
		map[string]string{"type": "text", "text": ""},
	}
	retained := []any{content[0], content[1], content[4]}
	manifest := map[string]any{
		"sessionId": sessionID, "callId": callID,
		"config": imageHookConfiguration(files),
		"before": map[string]any{"content": content, "value": map[string]any{"count": 0, "enabled": false}, "isError": false},
		"after":  map[string]any{"content": retained, "value": map[string]any{"count": 0, "enabled": false}, "isError": false},
		"images": map[string]any{
			first.ID:  map[string]any{"ref": first, "data": base64.StdEncoding.EncodeToString(firstBytes)},
			second.ID: map[string]any{"ref": second, "data": base64.StdEncoding.EncodeToString(secondBytes)},
		},
	}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestPath := filepath.Join(directory, "native-images.json")
	require.NoError(t, os.WriteFile(manifestPath, raw, 0o600))
	script := `
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {pathToFileURL} from 'node:url';
const [modulePath, manifestPath] = process.argv.slice(1);
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
const handlers = new Map();
const ctx = {
  on(event, handler) { handlers.set(event, handler); },
  get(name) {
    if (name === 'fs') return {processPathFromHostPath(path) {return path;}};
    if (name === 'attachments') return {
      imageHostPath(ref) {return join(manifest.config.temporaryDirectory, ref.attachmentId.slice(7) + '.png');},
      async readImage(ref) {
        const image = manifest.images[ref.attachmentId];
        return {ref: image.ref, data: Buffer.from(image.data, 'base64')};
      },
    };
  },
  logger: {warn(message, error) {throw new Error(message, {cause: error});}},
};
const {apply} = await import(pathToFileURL(modulePath).href);
apply(ctx, manifest.config);
const execution = {token: Symbol(), agent: {session: {id: manifest.sessionId}}, callId: manifest.callId, name: 'mcp__results__inspect'};
await handlers.get('tools/post-execute')(execution, manifest.before, async () => ({kind: 'accept', content: manifest.after.content}));
handlers.get('tools/result')(execution, manifest.after);
handlers.get('dispose')();
`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, filepath.Join(directory, "image_hook.mjs"), manifestPath)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	raw, err = os.ReadFile(filepath.Join(files.Receipts, imageReceiptLeaf(sessionID, callID)))
	require.NoError(t, err)
	var receipt imageReceipt
	require.NoError(t, json.Unmarshal(raw, &receipt))
	require.Len(t, receipt.Images, 2, "each native attachment needs one proof even when its ordered content repeats")
	assert.Equal(t, first, receipt.Images[0].Attachment)
	assert.Equal(t, second, receipt.Images[1].Attachment)
	require.Len(t, receipt.OriginalImages, 3)
	require.NotNil(t, receipt.RetainedImages)
	require.Len(t, *receipt.RetainedImages, 2)
	assert.Equal(t, 1, *receipt.OriginalImages[0].Position)
	assert.Equal(t, 3, *receipt.OriginalImages[1].Position)
	assert.Equal(t, 4, *receipt.OriginalImages[2].Position)
	assert.Equal(t, first, receipt.OriginalImages[0].Attachment)
	assert.Equal(t, second, receipt.OriginalImages[1].Attachment)
	assert.Equal(t, first, receipt.OriginalImages[2].Attachment)
	assert.Equal(t, 1, *(*receipt.RetainedImages)[0].Position)
	assert.Equal(t, 2, *(*receipt.RetainedImages)[1].Position)
	assert.NotContains(t, string(raw), `"before"`)
	assert.NotContains(t, string(raw), `"after"`)
	assert.NotContains(t, string(raw), `"formatted"`)
	extra, err := receiptImageSupplement(receipt, sessionID, callID, 1<<20)
	require.NoError(t, err)
	assert.Contains(t, string(extra), first.ID)
	assert.Contains(t, string(extra), second.ID)
}
