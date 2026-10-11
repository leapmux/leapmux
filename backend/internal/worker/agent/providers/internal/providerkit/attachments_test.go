package providerkit

import (
	"testing"

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
				Kind:     agent.AttachmentKindText,
			})
			assert.Equal(t, tc.want, got)
		})
	}
}
