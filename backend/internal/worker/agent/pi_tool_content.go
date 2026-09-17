package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
)

// piToolArtifactSource is the part of one tool_execution_end frame that an artifact
// resolve reads.
//
// It embeds contracts.PiToolCallIdentity for the two identity words, which
// piToolExecutionEnvelope (pi_output.go) reads as well. A Go struct tag cannot hold
// a constant, so each decoder spelled them by hand and a rename of either word in
// `resultFields` moved one decoder and left the other.
//
// `result` stays a tag here: this reader takes its MEMBERS and
// piToolExecutionEnvelope takes the whole value, so the two give it different Go
// types and one shared field cannot serve both.
type piToolArtifactSource struct {
	contracts.PiToolCallIdentity
	Type    string                     `json:"type"`
	Result  map[string]json.RawMessage `json:"result"`
	details map[string]json.RawMessage
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
		json.Unmarshal(source.Result[contracts.PiResultFieldDetails], &source.details) != nil || source.details == nil {
		return nil
	}
	return &source
}

func (source *piToolArtifactSource) outputReference() piArtifactReference {
	// `originalBytes` stays a tag here: Go alone reads it, so it crosses no language
	// boundary and the contract holds no entry for it.
	var guard struct {
		contracts.PiOutputGuard
		OriginalBytes json.RawMessage `json:"originalBytes"`
	}
	if json.Unmarshal(source.details[contracts.PiResultFieldOutputGuard], &guard) != nil || !guard.Truncated {
		return piArtifactReference{}
	}
	return piArtifactReference{path: guard.FullOutputPath, bytes: guard.OriginalBytes}
}

func (source *piToolArtifactSource) mcpResultReference() piArtifactReference {
	// `rawResultBytes` stays a tag, for the reason `outputReference` gives for
	// `originalBytes`.
	var omission struct {
		contracts.PiMcpResultOmission
		RawResultBytes json.RawMessage `json:"rawResultBytes"`
	}
	if json.Unmarshal(source.details[contracts.PiResultFieldMcpResult], &omission) != nil || !omission.Omitted ||
		len(omission.Content) > 0 || len(omission.Contents) > 0 {
		return piArtifactReference{}
	}
	return piArtifactReference{path: omission.FullResultPath, bytes: omission.RawResultBytes}
}

func (source *piToolArtifactSource) outputArtifact(raw json.RawMessage) *contracts.PiOutputArtifact {
	var artifact contracts.PiOutputArtifact
	path := source.outputReference().path
	if path == "" || json.Unmarshal(raw, &artifact) != nil || artifact.Text == nil || artifact.Path != path {
		return nil
	}
	return &artifact
}

func (source *piToolArtifactSource) mcpResultArtifact(raw json.RawMessage) *contracts.PiMcpResultArtifact {
	var artifact contracts.PiMcpResultArtifact
	var result map[string]json.RawMessage
	path := source.mcpResultReference().path
	if path == "" || json.Unmarshal(raw, &artifact) != nil || artifact.Path != path ||
		json.Unmarshal(artifact.Result, &result) != nil || result == nil {
		return nil
	}
	return &artifact
}

// Restore the provider's truncated text without changing its image order or block metadata.
func (source *piToolArtifactSource) restoreOutput(artifact *contracts.PiOutputArtifact) bool {
	var blocks []json.RawMessage
	if artifact == nil || artifact.Text == nil || json.Unmarshal(source.Result[contracts.PiResultFieldContent], &blocks) != nil || len(blocks) == 0 {
		return false
	}
	var first map[string]json.RawMessage
	var kind string
	var text *string
	if json.Unmarshal(blocks[0], &first) != nil || first == nil ||
		json.Unmarshal(first[contracts.PiContentBlockType], &kind) != nil || kind != contracts.PiBlockTypeText ||
		json.Unmarshal(first[contracts.PiContentBlockText], &text) != nil || text == nil {
		return false
	}
	if *text == *artifact.Text {
		return false
	}
	encodedText, err := json.Marshal(*artifact.Text)
	if err != nil {
		return false
	}
	first[contracts.PiContentBlockText] = encodedText
	encodedBlock, err := json.Marshal(first)
	if err != nil {
		return false
	}
	blocks[0] = encodedBlock
	encodedContent, err := json.Marshal(blocks)
	if err != nil {
		return false
	}
	source.Result[contracts.PiResultFieldContent] = encodedContent
	return true
}

// buildPiIncompleteToolSupplement encodes the partial result, or nothing when the
// call reported none.
func buildPiIncompleteToolSupplement(toolCallID string, tool piToolState) ([]byte, error) {
	if len(tool.PartialResult) == 0 {
		return nil, nil
	}
	return json.Marshal(contracts.PiIncompleteToolSupplement{
		ToolCallID:    toolCallID,
		ToolName:      tool.ToolName,
		PartialResult: tool.PartialResult,
	})
}

// resolvePiIncompleteTool puts a retained call's partial result on its start frame.
//
// The identity keys are checked first: a supplement that states another call, or
// another tool, cannot reach this row.
//
// The keys that index PI'S OWN frame -- here and in ResolveProviderData below -- come
// from the `resultFields` table, which holds the words Pi spells. The sibling
// PiSupplement* and PiArtifact* tables hold the words LEAPMUX chose for the envelope
// it stores beside that frame. The tables agree on the spelling today, and they answer
// to different owners: a rename of a key LeapMux picked must not change how Pi's own
// message is read.
func resolvePiIncompleteTool(content MessageContent) []byte {
	var extra contracts.PiIncompleteToolSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil ||
		extra.ToolCallID == "" || len(extra.PartialResult) == 0 {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil {
		return content.Original
	}
	var toolCallID, toolName string
	if json.Unmarshal(original[contracts.PiResultFieldToolCallID], &toolCallID) != nil || toolCallID != extra.ToolCallID {
		return content.Original
	}
	if json.Unmarshal(original[contracts.PiResultFieldToolName], &toolName) != nil || toolName != extra.ToolName {
		return content.Original
	}
	// Resolving an already-resolved frame must return the SAME bytes, so a caller
	// that resolves twice does not allocate a second copy of the row.
	if jsonEqual(original[contracts.PiResultFieldResult], extra.PartialResult) {
		return content.Original
	}
	original[contracts.PiResultFieldResult] = extra.PartialResult
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
	var extra contracts.PiToolArtifactSupplement
	if source == nil || json.Unmarshal(content.Supplemental, &extra) != nil ||
		extra.ToolCallID != source.ToolCallID || extra.ToolName != source.ToolName {
		return content.Original
	}
	changed := source.restoreOutput(source.outputArtifact(extra.OutputFile))
	if artifact := source.mcpResultArtifact(extra.McpResultFile); artifact != nil {
		source.details[contracts.PiResultFieldMcpResult] = artifact.Result
		changed = true
	}
	if !changed {
		return content.Original
	}
	details, err := json.Marshal(source.details)
	if err != nil {
		return content.Original
	}
	source.Result[contracts.PiResultFieldDetails] = details
	result, err := json.Marshal(source.Result)
	if err != nil {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil {
		return content.Original
	}
	original[contracts.PiResultFieldResult] = result
	resolved, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return resolved
}
