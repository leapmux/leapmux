package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestBuildInlineTextAttachmentBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		filename string
		mimeType string
		data     []byte
		want     string
	}{
		{
			name:     "wraps the file in an attached-file element that states its name and MIME type",
			filename: "notes.txt",
			mimeType: "text/plain",
			data:     []byte("line one\nline two\n"),
			want:     "<attached-file name=\"notes.txt\" mime-type=\"text/plain\">\nline one\nline two\n</attached-file>",
		},
		{
			name:     "ends content that has no final newline with one, so the closing tag stays on its own line",
			filename: "note.md",
			mimeType: "text/markdown",
			data:     []byte("# Title"),
			want:     "<attached-file name=\"note.md\" mime-type=\"text/markdown\">\n# Title\n</attached-file>",
		},
		{
			name:     "keeps an empty file as one empty line",
			filename: "empty.txt",
			mimeType: "text/plain",
			want:     "<attached-file name=\"empty.txt\" mime-type=\"text/plain\">\n\n</attached-file>",
		},
		{
			name:     "escapes the markup characters of the attribute values",
			filename: `a "b" <c> & d.txt`,
			mimeType: `text/plain; x="y"`,
			data:     []byte("x\n"),
			want:     "<attached-file name=\"a &quot;b&quot; &lt;c&gt; &amp; d.txt\" mime-type=\"text/plain; x=&quot;y&quot;\">\nx\n</attached-file>",
		},
		{
			name:     "escapes line breaks and tabs in a file name, so the opening tag stays on one line",
			filename: "two\nlines\tand\rreturn.txt",
			mimeType: "text/plain",
			data:     []byte("x\n"),
			want:     "<attached-file name=\"two&#10;lines&#9;and&#13;return.txt\" mime-type=\"text/plain\">\nx\n</attached-file>",
		},
		{
			name:     "keeps the file content byte for byte, markup included",
			filename: "page.html",
			mimeType: "text/html",
			data:     []byte("<b>&amp;</b>\n</attached-file>\n"),
			want:     "<attached-file name=\"page.html\" mime-type=\"text/html\">\n<b>&amp;</b>\n</attached-file>\n</attached-file>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildInlineTextAttachmentBlock(agent.ClassifiedAttachment{
				Filename: tc.filename,
				MIMEType: tc.mimeType,
				Data:     tc.data,
				Kind:     contracts.AttachmentMediaKindText,
			})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateAudioAttachments(t *testing.T) {
	t.Parallel()
	audio := func(name string) agent.ClassifiedAttachment {
		return agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindAudio, Filename: name, MIMEType: "audio/wav", Data: []byte{0, 255, 1}}
	}
	for name, attachments := range map[string][]agent.ClassifiedAttachment{
		"empty":     nil,
		"non audio": {{Kind: contracts.AttachmentMediaKindImage, Filename: "image"}},
		"mixed":     {audio("first"), {Kind: contracts.AttachmentMediaKindText, Filename: "text"}, audio("second")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var validated []string
			found, err := ValidateAudioAttachments(attachments, func(attachment agent.ClassifiedAttachment) error {
				validated = append(validated, attachment.Filename)
				assert.Equal(t, []byte{0, 255, 1}, attachment.Data)
				return nil
			})
			assert.NoError(t, err)
			assert.Equal(t, name == "mixed", found)
			if name == "mixed" {
				assert.Equal(t, []string{"first", "second"}, validated)
			} else {
				assert.Empty(t, validated)
			}
		})
	}
	t.Run("stops at the first provider refusal", func(t *testing.T) {
		var names []string
		found, err := ValidateAudioAttachments([]agent.ClassifiedAttachment{audio("first"), audio("second"), audio("third")}, func(attachment agent.ClassifiedAttachment) error {
			names = append(names, attachment.Filename)
			if attachment.Filename == "second" {
				return assert.AnError
			}
			return nil
		})
		assert.True(t, found)
		assert.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, []string{"first", "second"}, names)
	})
	t.Run("requires an explicit provider validator", func(t *testing.T) {
		found, err := ValidateAudioAttachments([]agent.ClassifiedAttachment{audio("first")}, nil)
		assert.False(t, found)
		assert.Error(t, err)
	})
}

func TestValidateAudioAttachment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		data    []byte
		mime    string
		allowed []string
		valid   bool
	}{
		{name: "accepted bytes", data: []byte{0, 255}, mime: "audio/wav", allowed: []string{"audio/ogg", "audio/wav"}, valid: true},
		{name: "absent bytes", mime: "audio/wav", allowed: []string{"audio/wav"}},
		{name: "empty bytes", data: []byte{}, mime: "audio/wav", allowed: []string{"audio/wav"}},
		{name: "unsupported media type", data: []byte{0}, mime: "audio/flac", allowed: []string{"audio/wav"}},
		{name: "absent media type", data: []byte{0}, allowed: []string{"audio/wav"}},
		{name: "absent policy", data: []byte{0}, mime: "audio/wav"},
		{name: "empty policy", data: []byte{0}, mime: "audio/wav", allowed: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAudioAttachment(agent.ClassifiedAttachment{Filename: "voice.wav", MIMEType: tc.mime, Data: tc.data}, "Test Agent audio attachment", tc.allowed)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, `cannot send Test Agent audio attachment "voice.wav" with media type `+tc.mime)
			}
		})
	}
}
