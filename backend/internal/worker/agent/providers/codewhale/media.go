package codewhale

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/imageheader"
)

// The native artifact route caps one read at 4 MiB. A native image can need two reads.
const mediaReadWindowBytes = 4 << 20
const mediaReplyMetadataBytes = 8 << 10

type mediaDescriptor struct {
	Version      int    `json:"version"`
	SessionID    string `json:"session_id"`
	OutputFileID string `json:"artifact_id"`
	ToolCallID   string `json:"tool_call_id"`
	MediaType    string `json:"media_type"`
	ByteSize     int    `json:"byte_size"`
	SHA256       string `json:"sha256"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type mediaSupplement struct {
	OutputFiles map[string]string `json:"outputFiles"`
}

type mediaOutputFile struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ContentType string `json:"content_type"`
	Kind        string `json:"kind"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	ByteSize    int    `json:"byte_size"`
}

type mediaWindow struct {
	OutputFile mediaOutputFile `json:"artifact"`
	Size       int             `json:"size"`
	Revision   string          `json:"revision"`
	Offset     *int            `json:"offset"`
	Bytes      int             `json:"bytes"`
	Truncated  *bool           `json:"truncated"`
	Encoding   string          `json:"encoding"`
	Content    string          `json:"content"`
}

func mediaSessionIDValid(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func mediaDigestValid(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (d mediaDescriptor) valid(callID string) bool {
	if d.Version != contracts.CodewhaleMediaRuleCurrentVersion || !mediaSessionIDValid(d.SessionID) || d.ToolCallID != callID || callID == "" {
		return false
	}
	if !strings.HasPrefix(d.OutputFileID, "art_image_") || !mediaDigestValid(strings.TrimPrefix(d.OutputFileID, "art_image_")) || !mediaDigestValid(d.SHA256) {
		return false
	}
	if d.ByteSize <= 0 || d.ByteSize > contracts.CodewhaleMediaRuleMaxImageBytes || d.Width <= 0 || d.Height <= 0 {
		return false
	}
	if d.Width > contracts.CodewhaleMediaRuleMaxDimension || d.Height > contracts.CodewhaleMediaRuleMaxDimension || int64(d.Width)*int64(d.Height) > contracts.CodewhaleMediaRuleMaxPixels {
		return false
	}
	return mediaMIMEValid(d.MediaType)
}

func mediaMIMEValid(mime string) bool {
	switch mime {
	case contracts.CodewhaleMediaTypePNG, contracts.CodewhaleMediaTypeJPEG, contracts.CodewhaleMediaTypeGIF, contracts.CodewhaleMediaTypeWebP:
		return true
	}
	return false
}

// recoverToolMedia retains immutable image bytes beside the original completed tool event.
func (a *Agent) recoverToolMedia(metadata itemMetadata, callID, toolName string, originalBytes int) []byte {
	if len(metadata.ToolMedia) == 0 || a.endpoint == nil {
		return nil
	}
	var descriptors []mediaDescriptor
	if json.Unmarshal(metadata.ToolMedia, &descriptors) != nil || len(descriptors) != 1 || !descriptors[0].valid(callID) {
		return nil
	}
	descriptor := descriptors[0]
	budget := agent.LiveMaxMessageSize() - originalBytes - mediaReplyMetadataBytes
	estimated := base64.StdEncoding.EncodedLen(descriptor.ByteSize) + len(descriptor.OutputFileID) + len(descriptor.MediaType) + len(`{"artifacts":{"":"data:;base64,"}}`)
	if originalBytes < 0 || budget < estimated {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.Context(), a.APITimeout())
	defer cancel()
	data, err := a.readToolMedia(ctx, descriptor, toolName)
	if err != nil {
		slog.Warn("codewhale image artifact read failed", "agent_id", a.AgentID(), "tool_call_id", callID, "error", err)
		return nil
	}
	supplement, err := json.Marshal(mediaSupplement{OutputFiles: map[string]string{descriptor.OutputFileID: "data:" + descriptor.MediaType + ";base64," + base64.StdEncoding.EncodeToString(data)}})
	if err != nil {
		slog.Warn("codewhale image supplement encode failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}
	if len(supplement) > budget {
		return nil
	}
	return supplement
}

func (a *Agent) readToolMedia(ctx context.Context, descriptor mediaDescriptor, toolName string) ([]byte, error) {
	data := make([]byte, 0, descriptor.ByteSize)
	for len(data) < descriptor.ByteSize {
		offset := len(data)
		limit := min(mediaReadWindowBytes, descriptor.ByteSize-offset)
		window, err := a.readMediaWindow(ctx, descriptor, offset, limit)
		if err != nil {
			return nil, err
		}
		outputFile := window.OutputFile
		if outputFile.ID != descriptor.OutputFileID || outputFile.SessionID != descriptor.SessionID || outputFile.ToolCallID != descriptor.ToolCallID || outputFile.ToolName != toolName || outputFile.ContentType != descriptor.MediaType || outputFile.Kind != "tool_output" || outputFile.ByteSize != descriptor.ByteSize || window.Size != descriptor.ByteSize || window.Revision != descriptor.SHA256 || window.Offset == nil || *window.Offset != offset || window.Bytes <= 0 || window.Bytes > limit || window.Truncated == nil {
			return nil, errors.New("the native image artifact window does not match its descriptor")
		}
		var part []byte
		switch window.Encoding {
		case "base64":
			part, err = base64.StdEncoding.Strict().DecodeString(window.Content)
		case "utf-8":
			part = []byte(window.Content)
		default:
			return nil, errors.New("the native image artifact encoding is unsupported")
		}
		if err != nil || (window.Encoding == "base64" && base64.StdEncoding.EncodeToString(part) != window.Content) || len(part) != window.Bytes || len(part) > limit || *window.Truncated != (offset+len(part) < descriptor.ByteSize) {
			return nil, errors.New("the native image artifact window has invalid content or truncation")
		}
		data = append(data, part...)
	}
	digest := sha256.Sum256(data)
	if len(data) != descriptor.ByteSize || hex.EncodeToString(digest[:]) != descriptor.SHA256 {
		return nil, errors.New("the native image artifact size or SHA256 does not match")
	}
	config, err := imageheader.Read(data)
	if err != nil || config.MIMEType != descriptor.MediaType || config.Width != descriptor.Width || config.Height != descriptor.Height {
		return nil, errors.New("the encoded native image does not match its declared type or dimensions")
	}
	return data, nil
}

func (a *Agent) readMediaWindow(ctx context.Context, descriptor mediaDescriptor, offset, limit int) (mediaWindow, error) {
	path := "/v1/sessions/" + descriptor.SessionID + "/artifacts/" + descriptor.OutputFileID
	response, err := a.endpoint.OpenStreamQuery(ctx, http.MethodGet, path, url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}, nil)
	if err != nil {
		return mediaWindow{}, err
	}
	defer func() { _ = response.Body.Close() }()
	maxJSON := ((limit+2)/3)*4 + mediaReplyMetadataBytes
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(maxJSON)+1))
	if err != nil {
		return mediaWindow{}, err
	}
	if len(raw) > maxJSON {
		return mediaWindow{}, errors.New("the native image artifact reply exceeds its byte limit")
	}
	var window mediaWindow
	if err := json.Unmarshal(raw, &window); err != nil {
		return mediaWindow{}, fmt.Errorf("the native image artifact reply is invalid: %w", err)
	}
	return window, nil
}
