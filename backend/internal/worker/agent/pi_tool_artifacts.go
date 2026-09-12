package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	piArtifactDirectory = regexp.MustCompile(`^pi-mcp-output-[A-Za-z0-9]{6}$`)
	piArtifactFile      = regexp.MustCompile(`^(output|mcp-result)-[0-9a-f]{8}\.txt$`)
)

// Recover only files that use pi-mcp-adapter's artifact format. The provider can use a custom temporary directory.
func readPiToolArtifact(ctx context.Context, ref piArtifactReference, kind string, maximum int) (data []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := ref.path
	directory, name := filepath.Dir(path), filepath.Base(path)
	if maximum <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!piArtifactDirectory.MatchString(filepath.Base(directory)) || !piArtifactFile.MatchString(name) || !strings.HasPrefix(name, kind+"-") {
		return nil, errors.New("invalid Pi artifact path or size limit")
	}
	var expected *int64
	if len(ref.bytes) > 0 && (json.Unmarshal(ref.bytes, &expected) != nil || expected == nil || *expected < 0) {
		return nil, errors.New("invalid Pi artifact byte count")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !directoryInfo.IsDir() {
		return nil, errors.New("the Pi artifact directory is not a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	openedDirectory, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(directoryInfo, openedDirectory) {
		return nil, errors.New("the Pi artifact directory changed during the read")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > int64(maximum) {
		return nil, errors.New("the Pi artifact is not a regular file within the size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("the Pi artifact changed during the read")
	}
	data, err = io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum || (expected != nil && int64(len(data)) != *expected) || !utf8.Valid(data) {
		return nil, errors.New("the Pi artifact has invalid content or a different byte count")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// Keep recovered snapshots when temporary files disappear before a later transcript boundary.
func recoverPiToolArtifacts(ctx context.Context, original, existing []byte) ([]byte, bool, error) {
	source := parsePiToolArtifactSource(original)
	if source == nil {
		return nil, true, nil
	}
	outputRef, mcpRef := source.outputReference(), source.mcpResultReference()
	if outputRef.path == "" && mcpRef.path == "" {
		return nil, true, nil
	}
	extra := piToolArtifactSupplement{ToolCallID: source.ToolCallID, ToolName: source.ToolName}
	var previous piToolArtifactSupplement
	if json.Unmarshal(existing, &previous) == nil && previous.ToolCallID == source.ToolCallID && previous.ToolName == source.ToolName {
		if source.outputArtifact(previous.OutputFile) != nil {
			extra.OutputFile = previous.OutputFile
		}
		if source.mcpResultArtifact(previous.McpResultFile) != nil {
			extra.McpResultFile = previous.McpResultFile
		}
	}
	maximum := liveStdoutMaxTokenSize() - len(original) - 1024
	initial, err := json.Marshal(extra)
	if err != nil {
		return nil, false, err
	}
	used := len(initial)
	store := func(field *json.RawMessage, artifact any) error {
		encoded, err := json.Marshal(artifact)
		if err != nil {
			return err
		}
		before := *field
		*field = encoded
		combined, err := json.Marshal(extra)
		if err != nil {
			*field = before
			return err
		}
		if len(combined) > maximum {
			*field = before
			return errors.New("the Pi tool supplement exceeds the message size limit")
		}
		used = len(combined)
		return nil
	}
	complete := true
	var failures error
	// The native MCP result keeps resources and structured data that flattened text cannot preserve.
	if ref := mcpRef; ref.path != "" && len(extra.McpResultFile) == 0 {
		data, err := readPiToolArtifact(ctx, ref, "mcp-result", maximum-used)
		if err == nil {
			var result map[string]json.RawMessage
			if json.Unmarshal(data, &result) != nil || result == nil {
				err = errors.New("the Pi MCP artifact is not a JSON object")
			} else {
				err = store(&extra.McpResultFile, piMcpResultArtifact{Path: ref.path, Result: data})
			}
		}
		if err != nil {
			complete = false
			failures = errors.Join(failures, err)
		}
	}
	if ref := outputRef; ref.path != "" && len(extra.OutputFile) == 0 {
		data, err := readPiToolArtifact(ctx, ref, "output", maximum-used)
		if err == nil {
			text := string(data)
			err = store(&extra.OutputFile, piOutputArtifact{Path: ref.path, Text: &text})
		}
		if err != nil {
			complete = false
			failures = errors.Join(failures, err)
		}
	}
	if len(extra.OutputFile) == 0 && len(extra.McpResultFile) == 0 {
		return nil, complete, failures
	}
	encoded, err := json.Marshal(extra)
	return encoded, complete, errors.Join(failures, err)
}
