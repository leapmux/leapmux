package muse

import (
	"encoding/base64"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputPartsRetainsTextAndImageBytes(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 255}
	parts, err := inputParts("", []*leapmuxv1.Attachment{{Filename: "proof.png", MimeType: "image/png", Data: data}})
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "", parts[0].Text)
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), parts[1].Base64Data)
	assert.Equal(t, "image/png", parts[1].MediaType)
}

func TestInputPartsRejectsUnsupportedBinaryPayloads(t *testing.T) {
	t.Parallel()
	for _, media := range []string{"application/pdf", "application/octet-stream"} {
		_, err := inputParts("proof", []*leapmuxv1.Attachment{{Filename: "proof.bin", MimeType: media, Data: []byte{255, 1}}})
		require.Error(t, err)
	}
}
