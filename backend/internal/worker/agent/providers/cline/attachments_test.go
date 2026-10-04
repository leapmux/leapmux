package cline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestValidateAttachment(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindText, Filename: "a.txt", Data: []byte("x")}))
	require.NoError(t, validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindText, Filename: "big.txt", Data: make([]byte, maxTextAttachmentBytes)}), "the limit itself is accepted")
	err := validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindText, Filename: "huge.txt", Data: make([]byte, maxTextAttachmentBytes+1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "huge.txt")
	require.NoError(t, validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindImage, Filename: "a.png"}))
	assert.ErrorContains(t, validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindPDF, Filename: "a.pdf"}), "PDF")
	assert.ErrorContains(t, validateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindBinary, Filename: "a.bin"}), "binary")
}

func TestAttachmentFileNameStaysInsideAndUnique(t *testing.T) {
	t.Parallel()
	taken := map[string]bool{}
	assert.Equal(t, "notes.md", attachmentFileName("notes.md", taken))
	assert.Equal(t, "notes-2.md", attachmentFileName("notes.md", taken))
	assert.Equal(t, "notes-3.md", attachmentFileName("dir/notes.md", taken))
	assert.Equal(t, "passwd", attachmentFileName("../../etc/passwd", taken), "a path cannot leave the directory")
	assert.Equal(t, "evil.txt", attachmentFileName(`..\..\evil.txt`, taken))
	assert.Equal(t, "attachment.txt", attachmentFileName("", taken))
	assert.Equal(t, "attachment-2.txt", attachmentFileName("..", taken))
	taken = map[string]bool{"a-2.txt": true}
	assert.Equal(t, "a.txt", attachmentFileName("a.txt", taken))
	assert.Equal(t, "a-3.txt", attachmentFileName("a.txt", taken), "a number another file took is skipped")
}

func TestBuildInputWritesEachTextFileOfAMessage(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	in, err := r.agent.buildInput("Read these.", []*leapmuxv1.Attachment{
		{Filename: "a.md", MimeType: "text/markdown", Data: []byte("A")},
		{Filename: "a.md", MimeType: "text/markdown", Data: []byte("B")},
	})
	require.NoError(t, err)
	require.Len(t, in.userFiles, 2)
	assert.Equal(t, "a.md", filepath.Base(in.userFiles[0]))
	assert.Equal(t, "a-2.md", filepath.Base(in.userFiles[1]))
	for i, want := range []string{"A", "B"} {
		data, err := os.ReadFile(in.userFiles[i])
		require.NoError(t, err)
		assert.Equal(t, want, string(data))
		assert.True(t, strings.HasPrefix(in.userFiles[i], r.agent.dir.Path()), "the files stay in the agent's directory")
	}
	second, err := r.agent.buildInput("", []*leapmuxv1.Attachment{{Filename: "a.md", MimeType: "text/markdown", Data: []byte("C")}})
	require.NoError(t, err)
	assert.NotEqual(t, filepath.Dir(in.userFiles[0]), filepath.Dir(second.userFiles[0]), "each message has a directory of its own")
}

func TestInputPayload(t *testing.T) {
	t.Parallel()
	plain := clineInput{prompt: "Hi"}.payload("s", sessionModeAct, "")
	assert.Equal(t, map[string]any{"sessionId": "s", "prompt": "Hi", "mode": sessionModeAct}, plain)
	steer := clineInput{prompt: "Also", userImages: []string{"data:image/png;base64,AA"}, userFiles: []string{"/f"}}.payload("s", sessionModePlan, deliverySteer)
	assert.Equal(t, deliverySteer, steer["delivery"])
	assert.Equal(t, map[string]any{"userImages": []string{"data:image/png;base64,AA"}, "userFiles": []string{"/f"}}, steer["attachments"])
}

// A text file that cannot be written fails the message before anything leaves.
func TestBuildInputFailsWhenItCannotWriteTheFile(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	// A file where the directory of the attachments belongs.
	require.NoError(t, os.WriteFile(filepath.Join(r.agent.dir.Path(), attachmentsDir), []byte("x"), 0o600))
	_, err := r.agent.buildInput("Read.", []*leapmuxv1.Attachment{{Filename: "a.md", MimeType: "text/markdown", Data: []byte("A")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prepare the attachment directory")
}

// A message of images alone needs no text, and writes no file.
func TestBuildInputTakesImagesAlone(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	in, err := r.agent.buildInput("", []*leapmuxv1.Attachment{
		{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
	})
	require.NoError(t, err)
	require.Len(t, in.userImages, 1)
	assert.Empty(t, in.userFiles)
	assert.NoDirExists(t, filepath.Join(r.agent.dir.Path(), attachmentsDir), "no text file, no directory")
}
