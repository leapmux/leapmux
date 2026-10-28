package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func fastagentInputString(fields map[string]json.RawMessage, key string, required bool) (string, bool, error) {
	raw, present := fields[key]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if required {
			return "", false, fmt.Errorf("the Fast Agent input has no %s", key)
		}
		return "", false, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false, fmt.Errorf("the Fast Agent input %s is not a string", key)
	}
	return value, true, nil
}

func fastagentInputAnnotations(raw json.RawMessage, fromACP bool) (map[string]any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("the Fast Agent input annotations are invalid")
	}
	result := make(map[string]any)
	if raw, present := fields["audience"]; present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var audience []string
		if json.Unmarshal(raw, &audience) != nil {
			return nil, errors.New("the Fast Agent input audience is invalid")
		}
		for _, role := range audience {
			if role != "user" && role != "assistant" {
				return nil, errors.New("the Fast Agent input audience has an unsupported role")
			}
		}
		if len(audience) > 0 {
			result["audience"] = audience
		}
	}
	if raw, present := fields["priority"]; present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var priority float64
		if json.Unmarshal(raw, &priority) != nil || priority < 0 || priority > 1 {
			return nil, errors.New("the Fast Agent input priority is invalid")
		}
		if priority == 0 {
			priority = 0
		}
		result["priority"] = priority
	}
	if !fromACP {
		for key, value := range fields {
			if key != "audience" && key != "priority" && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New("the native input has unexpected annotation data")
			}
		}
	}
	return result, nil
}

func normalizeFastagentInputBlock(raw json.RawMessage, fromACP bool) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("the Fast Agent input block is not an object")
	}
	kind, _, err := fastagentInputString(fields, "type", true)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"type": kind}
	allowed := map[string]bool{"type": true, "annotations": true, "_meta": true}
	copyString := func(key string, required bool) error {
		value, present, err := fastagentInputString(fields, key, required)
		if err != nil {
			return err
		}
		allowed[key] = true
		if present {
			result[key] = value
		}
		return nil
	}
	switch kind {
	case "text":
		if err := copyString("text", true); err != nil {
			return nil, err
		}
	case "image", "audio":
		if err := copyString("data", true); err != nil {
			return nil, err
		}
		if err := copyString("mimeType", true); err != nil {
			return nil, err
		}
		// Fast Agent's image constructor drops the ACP filename URI.
		if fromACP && kind == "image" {
			allowed["uri"] = true
		}
	case "resource":
		allowed["resource"] = true
		var resource map[string]json.RawMessage
		if json.Unmarshal(fields["resource"], &resource) != nil || resource == nil {
			return nil, errors.New("the Fast Agent embedded input resource is invalid")
		}
		uri, _, err := fastagentInputString(resource, "uri", true)
		if err != nil {
			return nil, err
		}
		native := map[string]any{"uri": uri}
		mime, present, err := fastagentInputString(resource, "mimeType", false)
		if err != nil {
			return nil, err
		}
		if present && (!fromACP || mime != "") {
			native["mimeType"] = mime
		}
		text, hasText, err := fastagentInputString(resource, "text", false)
		if err != nil {
			return nil, err
		}
		blob, hasBlob, err := fastagentInputString(resource, "blob", false)
		if err != nil {
			return nil, err
		}
		if hasText == hasBlob {
			return nil, errors.New("the Fast Agent input resource has no unique body")
		}
		if hasText {
			native["text"] = text
		} else {
			native["blob"] = blob
		}
		if !fromACP {
			for key, value := range resource {
				if key != "uri" && key != "mimeType" && key != "text" && key != "blob" && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return nil, errors.New("the native resource has unexpected input data")
				}
			}
		}
		result["resource"] = native
	default:
		return nil, fmt.Errorf("the Fast Agent input block type %q has no proof mapper", kind)
	}
	annotations, err := fastagentInputAnnotations(fields["annotations"], fromACP)
	if err != nil {
		return nil, err
	}
	if annotations != nil {
		result["annotations"] = annotations
	}
	if !fromACP {
		for key, value := range fields {
			if !allowed[key] {
				return nil, errors.New("the native input has an unexpected field")
			}
			if key == "_meta" && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New("the native input has unexpected metadata")
			}
		}
	}
	return result, nil
}

// The mapper follows Fast Agent's ACP content conversion. It compares known
// input fields and array order without changing the archived native bytes.
func fastagentInputDigest(blocks []json.RawMessage, fromACP bool) ([32]byte, error) {
	if blocks == nil {
		return [32]byte{}, errors.New("the Fast Agent input is not an explicit block array")
	}
	normalized := make([]map[string]any, 0, len(blocks))
	for _, raw := range blocks {
		block, err := normalizeFastagentInputBlock(raw, fromACP)
		if err != nil {
			return [32]byte{}, err
		}
		normalized = append(normalized, block)
	}
	if fromACP && len(normalized) > 0 && normalized[0]["type"] == "text" {
		text, _ := normalized[0]["text"].(string)
		if strings.HasPrefix(strings.TrimSpace(text), "/") {
			for _, block := range normalized[1:] {
				if block["type"] == "resource" {
					return [32]byte{}, errors.New("the Fast Agent slash-resource input requires native command conversion")
				}
			}
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return [32]byte{}, err
	}
	return agent.JSONCanonicalDigest(raw)
}
