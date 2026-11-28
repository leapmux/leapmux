package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOrderedObjectZeroValueIsAnEmptyObject(t *testing.T) {
	t.Parallel()
	var object OrderedObject
	assert.Equal(t, `{}`, string(object.Encode()))
	assert.Nil(t, object.Get("missing"))
	object.Set("b", json.RawMessage(`2`))
	object.Set("a", json.RawMessage(`1`))
	object.Set("b", json.RawMessage(`3`))
	assert.Equal(t, `{"b":3,"a":1}`, string(object.Encode()), "a key set again keeps its place and takes the new value")
	assert.JSONEq(t, `1`, string(object.Get("a")))
}

func TestReadOrderedObject(t *testing.T) {
	t.Parallel()
	object := ReadOrderedObject(json.RawMessage(`{ "z": { "deep": [1, 2] }, "a": "x", "z": "last" }`))
	assert.Equal(t, `{"z":"last","a":"x"}`, string(object.Encode()), "a key stated twice keeps its first place and its last value")

	for _, raw := range []string{``, `null`, `[]`, `"text"`, `{`, `{"a":}`, `not json`} {
		assert.Equal(t, `{}`, string(ReadOrderedObject(json.RawMessage(raw)).Encode()), "%q reads as an empty object", raw)
	}
}

func TestOrderedObjectMergeKeepsTheOrderOfEachSide(t *testing.T) {
	t.Parallel()
	var object OrderedObject
	object.Set("patterns", json.RawMessage(`["ls"]`))
	object.Set("cwd", json.RawMessage(`"/old"`))
	object.Merge(ReadOrderedObject(json.RawMessage(`{"timeout":5,"cwd":"/new"}`)))
	assert.Equal(t, `{"patterns":["ls"],"cwd":"/new","timeout":5}`, string(object.Encode()), "a merged key that the object holds keeps its place, as Object.assign keeps it")
}

func TestOrderedObjectMergeIgnoresNil(t *testing.T) {
	t.Parallel()
	var object OrderedObject
	object.Set("a", json.RawMessage(`1`))
	assert.NotPanics(t, func() { object.Merge(nil) })
	assert.Equal(t, `{"a":1}`, string(object.Encode()))
}

func TestOrderedObjectEncodesKeysAsJSONStringify(t *testing.T) {
	t.Parallel()
	var object OrderedObject
	object.Set("a<b>\u2028", json.RawMessage(`"v"`))
	assert.Equal(t, "{\"a<b>\u2028\":\"v\"}", string(object.Encode()))
}

func TestOrderedObjectEncodePanicsOnAValueThatIsNotJSON(t *testing.T) {
	t.Parallel()
	var object OrderedObject
	object.Set("a", json.RawMessage(`not json`))
	assert.Panics(t, func() { object.Encode() })
}
