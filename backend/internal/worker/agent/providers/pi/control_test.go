package pi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPiControlRequestKey(t *testing.T) {
	t.Parallel()

	key, ok := (piProvider{}).ControlRequestKey([]byte(`{"type":"extension_ui_request","id":"dlg-42","method":"confirm"}`), "")
	assert.True(t, ok)
	assert.Equal(t, "dlg-42", key)

	for name, payload := range map[string]string{
		"absent id":      `{"type":"extension_ui_request","method":"confirm"}`,
		"empty id":       `{"type":"extension_ui_request","id":"","method":"confirm"}`,
		"numeric id":     `{"type":"extension_ui_request","id":42}`,
		"boolean id":     `{"type":"extension_ui_request","id":true}`,
		"null id":        `{"type":"extension_ui_request","id":null}`,
		"object id":      `{"type":"extension_ui_request","id":{"nested":"dlg-1"}}`,
		"array id":       `{"type":"extension_ui_request","id":["dlg-1"]}`,
		"malformed json": `{"id":"dlg-1"`,
		"array json":     `["dlg-1"]`,
		"string json":    `"dlg-1"`,
		"number json":    `123`,
		"null json":      `null`,
		"empty bytes":    ``,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, found := (piProvider{}).ControlRequestKey([]byte(payload), "ignored")
			assert.False(t, found)
			assert.Empty(t, got)
		})
	}
}
