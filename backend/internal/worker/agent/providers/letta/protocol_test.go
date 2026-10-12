package letta

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestLettaClientMessageIDsAreUniqueAndRecognized(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := newLettaClientMessageID()

		assert.True(t, isLeapMuxClientMessageID(id), id)
		assert.NotContains(t, seen, id, "the App Server drops a message whose id it accepted before")
		seen[id] = struct{}{}
	}
}

func TestIsLeapMuxClientMessageIDRejectsIDsOfOtherSenders(t *testing.T) {
	t.Parallel()
	random := uuid.NewString()
	for name, id := range map[string]string{
		"an empty id":                    "",
		"a random id of Letta Code":      random,
		"the id of a verbatim echo":      "aa72b91d-0de8-4bde-bada-4d070e2a13f1",
		"a queue id of Letta Code":       "cm-submit-" + random,
		"the prefix alone":               lettaClientMessageIDPrefix,
		"the prefix and a word":          lettaClientMessageIDPrefix + "message",
		"the prefix and an id and text":  lettaClientMessageIDPrefix + random + "-more",
		"the prefix in another case":     "LEAPMUX-MESSAGE-" + random,
		"the id after a space":           " " + lettaClientMessageIDPrefix + random,
		"the prefix after another label": "x-" + lettaClientMessageIDPrefix + random,
		"an id in braces":                lettaClientMessageIDPrefix + "{" + random + "}",
		"an id in upper case":            lettaClientMessageIDPrefix + strings.ToUpper(random),
		"an id without hyphens":          lettaClientMessageIDPrefix + strings.ReplaceAll(random, "-", ""),
	} {
		assert.False(t, isLeapMuxClientMessageID(id), name)
	}
}
