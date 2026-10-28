package agent

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyAttachmentsInfersNativeAudio(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{"test.wav": "audio/wav", "test.MP3": "audio/mpeg", "test.m4a": "audio/mp4", "test.webm": "audio/webm", "test.ogg": "audio/ogg"} {
		for _, given := range []string{"", "application/octet-stream"} {
			data := []byte{0, 1, 2}
			attachments := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: given, Data: data}})
			require.Len(t, attachments, 1)
			assert.Equal(t, contracts.AttachmentMediaKindAudio, attachments[0].Kind)
			assert.Equal(t, mime, attachments[0].MIMEType)
			assert.Equal(t, data, attachments[0].Data)
		}
	}
}

func TestAudioHasAnIndependentAttachmentKind(t *testing.T) {
	t.Parallel()
	classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 1}}})
	require.Len(t, classified, 1)
	assert.Equal(t, contracts.AttachmentMediaKind("audio"), classified[0].Kind)
}

func TestClassifyAttachmentsInfersFLACAndAAC(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{"tone.flac": "audio/flac", "tone.AAC": "audio/aac"} {
		for _, supplied := range []string{"", "application/octet-stream"} {
			for _, data := range [][]byte{nil, []byte("audio bytes"), {0xff, 0}} {
				classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: supplied, Data: data}})
				require.Len(t, classified, 1)
				assert.Equal(t, contracts.AttachmentMediaKindAudio, classified[0].Kind, filename)
				assert.Equal(t, mime, classified[0].MIMEType, filename)
				assert.Equal(t, data, classified[0].Data)
			}
		}
		classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: "text/plain", Data: []byte("explicit text")}})
		require.Len(t, classified, 1)
		assert.Equal(t, contracts.AttachmentMediaKindText, classified[0].Kind)
	}
}

func TestClassifyAttachmentsInfersOfficeDocuments(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{
		"native.doc":  "application/msword",
		"native.DOCX": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"native.xls":  "application/vnd.ms-excel",
		"native.XLSX": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	} {
		for _, given := range []string{"", "application/octet-stream"} {
			for _, data := range [][]byte{nil, []byte("Office bytes"), {0xff, 0x00}} {
				classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: given, Data: data}})
				require.Len(t, classified, 1)
				assert.Equal(t, contracts.AttachmentMediaKindBinary, classified[0].Kind, filename)
				assert.Equal(t, mime, classified[0].MIMEType, filename)
				assert.Equal(t, data, classified[0].Data)
			}
		}
	}
	classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "native.docx", MimeType: "text/plain", Data: []byte("explicit text")}})
	require.Len(t, classified, 1)
	assert.Equal(t, contracts.AttachmentMediaKindText, classified[0].Kind, "an explicit text MIME remains authoritative")
}
