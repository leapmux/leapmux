package qwen

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// qwenModes lists Qwen's approval modes in the order its own session reports
// them. The session replaces this list as soon as it reports its own.
func qwenModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: contracts.QwenModePlan, Name: "Plan", Description: "Analyze only, do not modify files or execute commands"},
		{Id: contracts.QwenModeDefault, Name: "Default", Description: "Require approval for file edits or shell commands"},
		{Id: contracts.QwenModeAutoEdit, Name: "Auto Edit", Description: "Automatically approve file edits"},
		{Id: contracts.QwenModeAuto, Name: "Auto", Description: "A classifier model approves safe actions and blocks risky ones"},
		{Id: contracts.QwenModeYolo, Name: "YOLO", Description: "Approve every action without asking"},
	}
}

// launchApprovalMode is the mode that the launch flag states: the stored mode
// when Qwen knows it, else the safe default. Qwen's own default is `auto`,
// which sends every tool call to a classifier model, so the flag is always
// stated.
func launchApprovalMode(requested string) string {
	if slices.ContainsFunc(qwenModes(), func(option *leapmuxv1.AvailableOption) bool { return option.GetId() == requested }) {
		return requested
	}
	return contracts.QwenModeDefault
}

// decorateModel reads the context window Qwen states in a model's `_meta`.
func decorateModel(model *agent.ModelInfo, meta json.RawMessage) {
	var info struct {
		ContextLimit int64 `json:"contextLimit"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &info) != nil || info.ContextLimit <= 0 {
		return
	}
	model.ContextWindow = info.ContextLimit
}
