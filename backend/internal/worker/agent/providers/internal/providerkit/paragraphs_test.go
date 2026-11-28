package providerkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJoinParagraphs(t *testing.T) {
	for name, tc := range map[string]struct {
		parts []string
		want  string
	}{
		"nothing":                         {nil, ""},
		"one part":                        {[]string{"A"}, "A"},
		"two parts":                       {[]string{"A", "B"}, "A\n\nB"},
		"a part that ends with a newline": {[]string{"A\n", "B"}, "A\n\nB"},
		"a part that ends with two":       {[]string{"A\n\n", "B"}, "A\n\nB"},
		"a longer run stays":              {[]string{"A\n\n\n", "B"}, "A\n\n\nB"},
		"empty parts are skipped":         {[]string{"", "A", "", "B", ""}, "A\n\nB"},
		"a part of white space counts":    {[]string{"A", " ", "B"}, "A\n\n \n\nB"},
		"only newlines in the text":       {[]string{"\n", "B"}, "\n\nB"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, JoinParagraphs(tc.parts))
		})
	}
}
