package providerkit

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestTextOnlySteer(t *testing.T) {
	t.Parallel()
	assert.NoError(t, TextOnlySteer("Kiro", nil))
	attachment := &leapmuxv1.Attachment{Filename: "probe.txt", MimeType: "text/markdown", Data: []byte("content")}
	err := TextOnlySteer("Kiro", []*leapmuxv1.Attachment{attachment})
	assert.ErrorIs(t, err, agent.ErrSteeringUnsupported)
	assert.Contains(t, err.Error(), "Kiro steers a running turn with text alone")
}
