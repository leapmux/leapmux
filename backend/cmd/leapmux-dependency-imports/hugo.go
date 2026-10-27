package main

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/module"
)

func yamlValue(node *yaml.Node) (*yaml.Node, error) {
	seen := map[*yaml.Node]bool{}
	for node != nil && (node.Kind == yaml.AliasNode || node.Kind == yaml.DocumentNode) {
		if seen[node] {
			return nil, errors.New("cyclic Hugo configuration alias")
		}
		seen[node] = true
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		} else if len(node.Content) > 0 {
			node = node.Content[0]
		} else {
			return nil, nil
		}
	}
	if node != nil && (node.Kind == 0 || node.Tag == "!!null") {
		return nil, nil
	}
	return node, nil
}

func yamlMappingValue(node *yaml.Node, key string) (*yaml.Node, error) {
	node, err := yamlValue(node)
	if err != nil || node == nil {
		return nil, err
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the Hugo configuration containing %s must be a mapping", key)
	}
	var found *yaml.Node
	for index := 0; index+1 < len(node.Content); index += 2 {
		if strings.EqualFold(node.Content[index].Value, key) {
			if found != nil {
				return nil, fmt.Errorf("the Hugo configuration repeats %s", key)
			}
			found = node.Content[index+1]
		}
	}
	return found, nil
}

func yamlScalarSpan(source []byte, node *yaml.Node) (int, int, error) {
	start, line, column := -1, 1, 1
	for offset, character := range string(source) {
		if offset == 0 && character == '\ufeff' {
			continue
		}
		if line == node.Line && column == node.Column {
			start = offset
			break
		}
		if character == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	if start < 0 {
		return 0, 0, errors.New("cannot locate the Hugo import scalar")
	}
	// YAML properties precede the scalar. Leave its anchor and explicit tag intact.
	for start < len(source) && (source[start] == '&' || source[start] == '!') {
		for start < len(source) && source[start] > ' ' {
			start++
		}
		for start < len(source) && source[start] <= ' ' {
			start++
		}
	}
	if start >= len(source) {
		return 0, 0, errors.New("the Hugo import scalar is empty")
	}
	if node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			return 0, 0, errors.New("the Hugo import block has no value")
		}
		start += end + 1
		for start < len(source) && (source[start] == ' ' || source[start] == '\t') {
			start++
		}
		end = start + len(node.Value)
		if end > len(source) || string(source[start:end]) != node.Value {
			return 0, 0, errors.New("cannot locate the Hugo import block value")
		}
		return start, end, nil
	}
	quote := source[start]
	if quote == '\'' || quote == '"' {
		for end := start + 1; end < len(source); end++ {
			if quote == '"' && source[end] == '\\' {
				end++
				continue
			}
			if source[end] == quote {
				if quote == '\'' && end+1 < len(source) && source[end+1] == quote {
					end++
					continue
				}
				return start, end + 1, nil
			}
		}
		return 0, 0, errors.New("the Hugo import scalar has no closing quote")
	}
	end := start
	for end < len(source) && source[end] > ' ' && !strings.ContainsRune(",]}", rune(source[end])) {
		end++
	}
	if end == start {
		return 0, 0, errors.New("cannot locate the Hugo import value")
	}
	return start, end, nil
}

func rewriteHugoImports(source []byte, changes []moduleChange, roots []string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("parse Hugo configuration: %w", err)
	}
	configuration, err := yamlMappingValue(&document, "module")
	if err != nil {
		return nil, err
	}
	imports, err := yamlMappingValue(configuration, "imports")
	if err != nil {
		return nil, err
	}
	imports, err = yamlValue(imports)
	if err != nil || imports == nil {
		return source, err
	}
	if imports.Kind != yaml.SequenceNode {
		return nil, errors.New("the Hugo module imports must be a sequence")
	}
	seen := map[*yaml.Node]bool{}
	var edits []textEdit
	for _, entry := range imports.Content {
		path, err := yamlMappingValue(entry, "path")
		if err != nil {
			return nil, err
		}
		if path == nil || seen[path] {
			continue
		}
		seen[path] = true
		value, err := yamlValue(path)
		if err != nil {
			return nil, err
		}
		if value == nil || value.Kind != yaml.ScalarNode {
			return nil, errors.New("a Hugo module import path must be a scalar")
		}
		updated := rewritePath(value.Value, changes, roots)
		if updated == value.Value {
			continue
		}
		if err := module.CheckImportPath(updated); err != nil {
			return nil, fmt.Errorf("invalid migrated Hugo import: %w", err)
		}
		start, end, err := yamlScalarSpan(source, path)
		if err != nil {
			return nil, err
		}
		if path.Kind == yaml.AliasNode {
			if string(source[start:end]) != "*"+path.Value {
				return nil, errors.New("the Hugo import alias position differs from its source")
			}
		} else {
			var original string
			if err := yaml.Unmarshal(source[start:end], &original); err != nil || original != value.Value {
				return nil, errors.New("the Hugo import value position differs from its source")
			}
		}
		if path.Kind != yaml.AliasNode {
			if path.Style&yaml.DoubleQuotedStyle != 0 {
				updated = strconv.Quote(updated)
			} else if path.Style&yaml.SingleQuotedStyle != 0 {
				updated = "'" + strings.ReplaceAll(updated, "'", "''") + "'"
			}
		}
		edits = append(edits, textEdit{start, end, updated})
	}
	return applyEdits(source, edits), nil
}
