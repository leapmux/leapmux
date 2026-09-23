//go:build unix

package pi

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAttachmentsForProvider_PiRejectsPDFAndBinary(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		att  *leapmuxv1.Attachment
		want string
	}{
		"pdf":    {&leapmuxv1.Attachment{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}, "pi does not support PDF attachments"},
		"binary": {&leapmuxv1.Attachment{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}, "pi does not support binary attachments"},
	}
	for kind, tc := range cases {
		t.Run(kind, func(t *testing.T) {
			_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, []*leapmuxv1.Attachment{tc.att})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
