package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

const geminiPlanTitlePrefix = "Requesting plan approval for: "
const geminiPlanReadLimit = 4 << 20

type geminiControlServices struct {
	agent.ProviderServices
	query          agent.StoredSessionQuery
	currentSession func() string
	transcript     *tooltranscript.Transcript
}

// PublishControlRequest links the native plan request to its exact enriched source.
func (services *geminiControlServices) PublishControlRequest(request agent.ControlRequest) error {
	callID, planPath, sessionID, plan := geminiPlanPermission(request.Payload)
	if !plan {
		return services.ProviderServices.PublishControlRequest(request)
	}
	if services.currentSession == nil || sessionID == "" || sessionID != services.currentSession() {
		return errors.New("the Gemini plan request belongs to another session")
	}
	source, err := services.ReadToolRequest(callID)
	if err != nil {
		return fmt.Errorf("read the Gemini plan source: %w", err)
	}
	if source == nil || source.Seq <= 0 || !geminiPlanSourceMatches(source.Content.Original, callID, planPath) {
		return errors.New("the Gemini plan request has no matching native source")
	}
	content, err := readGeminiPlan(services.query, sessionID, planPath)
	if err != nil {
		return fmt.Errorf("read the Gemini plan: %w", err)
	}
	if services.transcript == nil {
		return errors.New("the Gemini plan source has no transcript")
	}
	_, err = services.transcript.EnrichToolSpan(callID, func(original []byte) ([]byte, error) {
		if !geminiPlanSourceMatches(original, callID, planPath) {
			return nil, errors.New("the Gemini plan source changed")
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(original, &frame); err != nil {
			return nil, err
		}
		supplement := acp.NewToolSupplement(frame)
		if err := setGeminiPlanSupplement(supplement, planPath, content); err != nil {
			return nil, err
		}
		return json.Marshal(supplement)
	})
	if err != nil {
		return fmt.Errorf("store the Gemini plan source: %w", err)
	}
	// An unchanged supplement returns false too. Read the actual source before publication.
	source, err = services.ReadToolRequest(callID)
	if err != nil {
		return fmt.Errorf("read the enriched Gemini plan source: %w", err)
	}
	if source == nil || !geminiPlanSourceMatches(source.Content.Original, callID, planPath) || !geminiPlanSupplementMatches(source.Content.Supplemental, planPath, content) {
		return errors.New("the Gemini plan source did not retain its complete content")
	}
	request.SourceSeq = source.Seq
	compressed, compression := msgcodec.Compress(content)
	services.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(string(content)))
	return services.ProviderServices.PublishControlRequest(request)
}

func geminiPlanPermission(payload []byte) (callID, planPath, sessionID string, plan bool) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
			ToolCall  struct {
				ToolCallID string `json:"toolCallId"`
				Title      string `json:"title"`
			} `json:"toolCall"`
		} `json:"params"`
	}
	if json.Unmarshal(payload, &request) != nil || request.Method != "session/request_permission" ||
		!strings.HasPrefix(request.Params.ToolCall.ToolCallID, contracts.GeminiToolExitPlanMode+"__") {
		return "", "", "", false
	}
	path, found := strings.CutPrefix(request.Params.ToolCall.Title, geminiPlanTitlePrefix)
	if !found || path == "" {
		return "", "", "", false
	}
	return request.Params.ToolCall.ToolCallID, path, request.Params.SessionID, true
}

func geminiPlanSourceMatches(original []byte, callID, planPath string) bool {
	var frame struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Title         string `json:"title"`
	}
	return json.Unmarshal(original, &frame) == nil && frame.SessionUpdate == contracts.ACPUpdateToolCall &&
		frame.ToolCallID == callID && frame.Title == geminiPlanTitlePrefix+planPath
}

func setGeminiPlanSupplement(supplement acp.ToolSupplement, path string, content []byte) error {
	for key, value := range map[string]string{contracts.GeminiSupplementPlanPath: path, contracts.GeminiSupplementPlanContent: string(content)} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		supplement[key] = encoded
	}
	return nil
}

func geminiPlanSupplementMatches(supplement []byte, path string, content []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(supplement, &fields) != nil {
		return false
	}
	var storedPath, storedContent string
	return json.Unmarshal(fields[contracts.GeminiSupplementPlanPath], &storedPath) == nil && storedPath == path &&
		json.Unmarshal(fields[contracts.GeminiSupplementPlanContent], &storedContent) == nil && storedContent == string(content)
}

func readGeminiPlan(query agent.StoredSessionQuery, sessionID, path string) (content []byte, err error) {
	if !validGeminiSessionID(sessionID) || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, errors.New("the Gemini plan path or session is invalid")
	}
	workingDir, _ := geminiProjectIdentity(query.WorkingDir)
	if workingDir == "" {
		return nil, errors.New("the Gemini plan has no working directory")
	}
	rootPath := workingDir
	parts, withinWorkspace := geminiPathParts(workingDir, path)
	if !withinWorkspace {
		project, locateErr := geminiProjectDirectory(query)
		if locateErr != nil {
			return nil, locateErr
		}
		rootPath = geminiConfigRoot(query)
		projectRelative, relativeErr := filepath.Rel(rootPath, filepath.Dir(project))
		if relativeErr != nil {
			return nil, relativeErr
		}
		plans := filepath.Join(rootPath, projectRelative, sessionID, "plans")
		if _, withinPlans := geminiPathParts(plans, path); !withinPlans {
			return nil, errors.New("the Gemini plan path belongs to another session or directory")
		}
		var withinRoot bool
		parts, withinRoot = geminiPathParts(rootPath, path)
		if !withinRoot {
			return nil, errors.New("the Gemini plan path leaves its configuration directory")
		}
	}
	root, err := sessionstore.OpenArchiveRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			content, err = nil, errors.Join(err, closeErr)
		}
	}()
	content, err = sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, geminiPlanReadLimit, parts...)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || !utf8.Valid(content) {
		return nil, errors.New("the Gemini plan content is empty or invalid UTF-8")
	}
	return content, nil
}

func geminiPathParts(root, path string) ([]string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		if !validGeminiPathComponent(part) {
			return nil, false
		}
	}
	return parts, true
}
