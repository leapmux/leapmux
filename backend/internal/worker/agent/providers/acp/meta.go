package acp

import "maps"

// metaKey is the field of the Agent Client Protocol that carries a
// runtime's own extensions of a message.
const metaKey = "_meta"

// WithMeta returns a copy of an Agent Client Protocol result whose `_meta`
// object holds value under key, or under the path that key and nested state,
// one key for each level below `_meta`.
//
// A runtime of the Agent Client Protocol family reads its own fields of a
// reply from `_meta`: Grok Build reads the reason of a refused permission as
// `_meta.followup_message`, and Kiro reads it as `_meta.kiro.rejectionReason`.
//
// Each level keeps every other key that it holds. A level that is absent, or
// that holds a value other than a map[string]any, becomes a new object, so a
// later value always reaches its place. The function never changes result or
// a map inside it, because the caller may hold them.
func WithMeta(result map[string]any, value any, key string, nested ...string) map[string]any {
	path := append([]string{metaKey, key}, nested...)
	return withObjectPath(result, value, path)
}

// withObjectPath returns a copy of object with value at path. path holds one
// key at least.
func withObjectPath(object map[string]any, value any, path []string) map[string]any {
	copied := maps.Clone(object)
	if copied == nil {
		copied = map[string]any{}
	}
	if len(path) == 1 {
		copied[path[0]] = value
		return copied
	}
	child, _ := object[path[0]].(map[string]any)
	copied[path[0]] = withObjectPath(child, value, path[1:])
	return copied
}
