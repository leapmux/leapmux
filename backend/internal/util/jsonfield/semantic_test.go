package jsonfield

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSemanticEqual(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, left, right string
		equal             bool
	}{
		{"object order", `{"a":[1,true,null],"b":"x"}`, `{"b":"x","a":[1.0,true,null]}`, true},
		{"array order", `[1,2]`, `[2,1]`, false},
		{"missing member", `{}`, `{"a":null}`, false},
		{"zero", `-0.000e999999999999999999999999999999`, `0`, true},
		{"negative sign", `-1`, `1`, false},
		{"decimal exponent", `12.3400e2`, `1234`, true},
		{"negative exponent", `1234e-2`, `12.34`, true},
		{"huge exponent", `1e999999999999999999999999999999`, `10e999999999999999999999999999998`, true},
		{"huge exponent differs", `1e999999999999999999999999999999`, `1e999999999999999999999999999998`, false},
		{"integer precision", `9007199254740992`, `9007199254740993`, false},
		{"large integer equivalent", `90071992547409930`, `9007199254740993e1`, true},
		{"nested precision", `{"a":[9007199254740992]}`, `{"a":[9007199254740993]}`, false},
		{"string numeric spelling", `"1"`, `"1.0"`, false},
		{"escaped string", `"\u0061"`, `"a"`, true},
		{"absent and null", "", `null`, false},
		{"invalid exact", `{broken`, `{broken`, true},
		{"invalid differing", `{broken`, ` {broken`, false},
		{"trailing value", `1 2`, `1`, false},
		{"boolean numeric", `true`, `1`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.equal, SemanticEqual(json.RawMessage(test.left), json.RawMessage(test.right)))
			assert.Equal(t, test.equal, SemanticEqual(json.RawMessage(test.right), json.RawMessage(test.left)))
		})
	}
}
