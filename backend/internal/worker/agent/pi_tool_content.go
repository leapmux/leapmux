package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
)

type piToolArtifactSupplement struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	OutputFile    json.RawMessage `json:"outputFile,omitempty"`
	McpResultFile json.RawMessage `json:"mcpResultFile,omitempty"`
}

type piOutputArtifact struct {
	Path string  `json:"path"`
	Text *string `json:"text"`
}

type piMcpResultArtifact struct {
	Path   string          `json:"path"`
	Result json.RawMessage `json:"result"`
}

type piToolArtifactSource struct {
	Type       string                     `json:"type"`
	ToolCallID string                     `json:"toolCallId"`
	ToolName   string                     `json:"toolName"`
	Result     map[string]json.RawMessage `json:"result"`
	details    map[string]json.RawMessage
}

type piArtifactReference struct {
	path  string
	bytes json.RawMessage
}

// piEventType reads one Pi event's discriminator, or the empty string.
func piEventType(raw []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Type
}

func parsePiToolArtifactSource(raw []byte) *piToolArtifactSource {
	var source piToolArtifactSource
	if json.Unmarshal(raw, &source) != nil || source.Type != contracts.PiEventToolExecutionEnd ||
		source.ToolCallID == "" || source.ToolName == "" || source.Result == nil ||
		json.Unmarshal(source.Result["details"], &source.details) != nil || source.details == nil {
		return nil
	}
	return &source
}

func (source *piToolArtifactSource) outputReference() piArtifactReference {
	var guard struct {
		Truncated      bool            `json:"truncated"`
		FullOutputPath string          `json:"fullOutputPath"`
		OriginalBytes  json.RawMessage `json:"originalBytes"`
	}
	if json.Unmarshal(source.details["outputGuard"], &guard) != nil || !guard.Truncated {
		return piArtifactReference{}
	}
	return piArtifactReference{path: guard.FullOutputPath, bytes: guard.OriginalBytes}
}

func (source *piToolArtifactSource) mcpResultReference() piArtifactReference {
	var omission struct {
		Omitted        bool            `json:"omitted"`
		FullResultPath string          `json:"fullResultPath"`
		RawResultBytes json.RawMessage `json:"rawResultBytes"`
		Content        json.RawMessage `json:"content"`
		Contents       json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(source.details["mcpResult"], &omission) != nil || !omission.Omitted ||
		len(omission.Content) > 0 || len(omission.Contents) > 0 {
		return piArtifactReference{}
	}
	return piArtifactReference{path: omission.FullResultPath, bytes: omission.RawResultBytes}
}

func (source *piToolArtifactSource) outputArtifact(raw json.RawMessage) *piOutputArtifact {
	var artifact piOutputArtifact
	path := source.outputReference().path
	if path == "" || json.Unmarshal(raw, &artifact) != nil || artifact.Text == nil || artifact.Path != path {
		return nil
	}
	return &artifact
}

func (source *piToolArtifactSource) mcpResultArtifact(raw json.RawMessage) *piMcpResultArtifact {
	var artifact piMcpResultArtifact
	var result map[string]json.RawMessage
	path := source.mcpResultReference().path
	if path == "" || json.Unmarshal(raw, &artifact) != nil || artifact.Path != path ||
		json.Unmarshal(artifact.Result, &result) != nil || result == nil {
		return nil
	}
	return &artifact
}

// Restore the provider's truncated text without changing its image order or block metadata.
func (source *piToolArtifactSource) restoreOutput(artifact *piOutputArtifact) bool {
	var blocks []json.RawMessage
	if artifact == nil || artifact.Text == nil || json.Unmarshal(source.Result["content"], &blocks) != nil || len(blocks) == 0 {
		return false
	}
	var first map[string]json.RawMessage
	var kind string
	var text *string
	if json.Unmarshal(blocks[0], &first) != nil || first == nil ||
		json.Unmarshal(first["type"], &kind) != nil || kind != "text" || json.Unmarshal(first["text"], &text) != nil || text == nil {
		return false
	}
	if *text == *artifact.Text {
		return false
	}
	encodedText, err := json.Marshal(*artifact.Text)
	if err != nil {
		return false
	}
	first["text"] = encodedText
	encodedBlock, err := json.Marshal(first)
	if err != nil {
		return false
	}
	blocks[0] = encodedBlock
	encodedContent, err := json.Marshal(blocks)
	if err != nil {
		return false
	}
	source.Result["content"] = encodedContent
	return true
}

// piIncompleteToolSupplement is the partial result a tool call reported before its
// turn ended.
//
// Pi puts a call's result on its tool_execution_end event and sends none when the
// turn ends first, so the last tool_execution_update is the only copy. The
// supplement names the call it belongs to, so a row cannot take another call's
// result.
type piIncompleteToolSupplement struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	PartialResult json.RawMessage `json:"partialResult,omitempty"`
}

// buildPiIncompleteToolSupplement encodes the partial result, or nothing when the
// call reported none.
func buildPiIncompleteToolSupplement(toolCallID string, tool piToolState) ([]byte, error) {
	if len(tool.PartialResult) == 0 {
		return nil, nil
	}
	return json.Marshal(piIncompleteToolSupplement{
		ToolCallID:    toolCallID,
		ToolName:      tool.ToolName,
		PartialResult: tool.PartialResult,
	})
}

// resolvePiIncompleteTool puts a retained call's partial result on its start frame.
//
// The identity keys are checked first: a supplement that names another call, or
// another tool, cannot reach this row.
func resolvePiIncompleteTool(content MessageContent) []byte {
	var extra piIncompleteToolSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil ||
		extra.ToolCallID == "" || len(extra.PartialResult) == 0 {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil {
		return content.Original
	}
	var toolCallID, toolName string
	if json.Unmarshal(original["toolCallId"], &toolCallID) != nil || toolCallID != extra.ToolCallID {
		return content.Original
	}
	if json.Unmarshal(original["toolName"], &toolName) != nil || toolName != extra.ToolName {
		return content.Original
	}
	// Resolving an already-resolved frame must return the SAME bytes, so a caller
	// that resolves twice does not allocate a second copy of the row.
	if jsonEqual(original["result"], extra.PartialResult) {
		return content.Original
	}
	original["result"] = extra.PartialResult
	resolved, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return resolved
}

// Resolve only artifacts that match the original call and its original artifact paths.
func (piProvider) ResolveProviderData(content MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	// A retained call's row is its START frame, which carries no result. The two
	// supplement shapes therefore never meet on one row.
	if piEventType(content.Original) == contracts.PiEventToolExecutionStart {
		return resolvePiIncompleteTool(content)
	}
	source := parsePiToolArtifactSource(content.Original)
	var extra piToolArtifactSupplement
	if source == nil || json.Unmarshal(content.Supplemental, &extra) != nil ||
		extra.ToolCallID != source.ToolCallID || extra.ToolName != source.ToolName {
		return content.Original
	}
	changed := source.restoreOutput(source.outputArtifact(extra.OutputFile))
	if artifact := source.mcpResultArtifact(extra.McpResultFile); artifact != nil {
		source.details["mcpResult"] = artifact.Result
		changed = true
	}
	if !changed {
		return content.Original
	}
	details, err := json.Marshal(source.details)
	if err != nil {
		return content.Original
	}
	source.Result["details"] = details
	result, err := json.Marshal(source.Result)
	if err != nil {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil {
		return content.Original
	}
	original["result"] = result
	resolved, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return resolved
}
