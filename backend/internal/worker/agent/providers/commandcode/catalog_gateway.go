package commandcode

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type gatewayModel struct {
	id            string
	contextWindow int64
	efforts       []string
}

// Command Code 1.73.2 supplies these selectable gateway models and their limits.
var nativeGatewayCatalog = []gatewayModel{
	{id: "claude-sonnet-5-5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-sonnet-5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-sonnet-4-6", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-fable-5-1", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-fable-5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-opus-5-5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-opus-5", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-opus-4-8", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-opus-4-7", contextWindow: 1000000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "claude-haiku-4-5-20251001", contextWindow: 200000, efforts: nil},
	{id: "gpt-6-astra", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-6.1-sol", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-6-sol", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-6-luna", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-5.6-sol", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-5.6-terra", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-5.6-luna", contextWindow: 1050000, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "gpt-5.5", contextWindow: 400000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "gpt-5.4", contextWindow: 400000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "gpt-5.3-codex", contextWindow: 400000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "gpt-5.4-mini", contextWindow: 400000, efforts: []string{"low", "medium", "high"}},
	{id: "moonshotai/Kimi-K3", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "thinkingmachines/inkling", contextWindow: 256000, efforts: nil},
	{id: "thinkingmachines/inkling-small", contextWindow: 1000000, efforts: nil},
	{id: "deepseek/deepseek-v4-pro", contextWindow: 1000000, efforts: []string{"high", "max"}},
	{id: "deepseek/deepseek-v4-flash", contextWindow: 1000000, efforts: []string{"high", "max"}},
	{id: "deepseek/deepseek-v4-flash-vision-exp", contextWindow: 1000000, efforts: []string{"high", "max"}},
	{id: "deepseek/deepseek-v4-flash-fast", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "deepseek/deepseek-v4.1-flash", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "deepseek/deepseek-v4.1-flash-fast", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "moonshotai/Kimi-K2.7-Code", contextWindow: 256000, efforts: nil},
	{id: "moonshotai/Kimi-K2.7-Code-Highspeed", contextWindow: 262000, efforts: nil},
	{id: "moonshotai/Kimi-K2.6", contextWindow: 256000, efforts: nil},
	{id: "moonshotai/Kimi-K2.5", contextWindow: 256000, efforts: nil},
	{id: "zai-org/GLM-5.3", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "z-ai/glm-5.3-flash", contextWindow: 1048576, efforts: []string{"low", "high", "max"}},
	{id: "z-ai/glm-5.3-flashx", contextWindow: 1000000, efforts: []string{"low", "high", "max"}},
	{id: "zai-org/GLM-5.2", contextWindow: 1000000, efforts: []string{"high", "max"}},
	{id: "zai-org/GLM-5.2-Fast", contextWindow: 1000000, efforts: nil},
	{id: "zai-org/GLM-5.1", contextWindow: 200000, efforts: nil},
	{id: "zai-org/GLM-5", contextWindow: 200000, efforts: nil},
	{id: "MiniMaxAI/MiniMax-M3", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "MiniMaxAI/MiniMax-M2.7", contextWindow: 200000, efforts: nil},
	{id: "MiniMaxAI/MiniMax-M2.5", contextWindow: 200000, efforts: nil},
	{id: "xiaomi/mimo-v2.6-pro", contextWindow: 1048576, efforts: nil},
	{id: "xiaomi/mimo-v2.6-pro-ultraspeed", contextWindow: 1048576, efforts: nil},
	{id: "xiaomi/mimo-v2.6-flash", contextWindow: 1048576, efforts: nil},
	{id: "xiaomi/mimo-v2.5-pro", contextWindow: 1000000, efforts: nil},
	{id: "xiaomi/mimo-v2.5", contextWindow: 1000000, efforts: nil},
	{id: "Qwen/Qwen3.6-Max-Preview", contextWindow: 200000, efforts: nil},
	{id: "Qwen/Qwen3.6-Plus", contextWindow: 200000, efforts: nil},
	{id: "Qwen/Qwen3.7-Max", contextWindow: 1000000, efforts: nil},
	{id: "Qwen/Qwen3.7-Plus", contextWindow: 1000000, efforts: nil},
	{id: "Qwen/Qwen3.8-Omni-Flash", contextWindow: 1000000, efforts: []string{"low", "medium", "xhigh"}},
	{id: "Qwen/Qwen3.8-Max-0902", contextWindow: 1000000, efforts: []string{"low", "medium", "xhigh"}},
	{id: "Qwen/Qwen3.8-Max", contextWindow: 1000000, efforts: []string{"low", "medium", "xhigh"}},
	{id: "Qwen/Qwen3.8-27B", contextWindow: 262144, efforts: []string{"low", "medium", "xhigh"}},
	{id: "Qwen/Qwen3.8-Flash", contextWindow: 1000000, efforts: []string{"low", "medium", "xhigh"}},
	{id: "Qwen/Qwen3.7-Flash", contextWindow: 1000000, efforts: nil},
	{id: "meituan/LongCat-2.0", contextWindow: 1048576, efforts: nil},
	{id: "stepfun/Step-5-Preview", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "stepfun/Step-3.7-Flash", contextWindow: 256000, efforts: nil},
	{id: "stepfun/Step-3.5-Flash", contextWindow: 262144, efforts: nil},
	{id: "tencent/hy4-preview", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{id: "tencent/hy3-paid", contextWindow: 262144, efforts: nil},
	{id: "google/gemini-3.8-flash", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "google/gemini-3.7-flash", contextWindow: 1048576, efforts: []string{"low", "medium", "high"}},
	{id: "google/gemini-3.6-flash", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "google/gemini-3.5-flash", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "google/gemini-3.5-flash-lite", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "google/gemini-3.1-flash-lite", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "sakana/fugu-ultra", contextWindow: 1000000, efforts: []string{"high", "xhigh"}},
	{id: "xai/grok-4.5", contextWindow: 500000, efforts: []string{"low", "medium", "high"}},
	{id: "xai/grok-4.6", contextWindow: 500000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "xai/grok-4.7", contextWindow: 500000, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "meta/muse-spark-1.1", contextWindow: 1048576, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "meta/muse-spark-1.2", contextWindow: 1048576, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "meta/muse-spark-1.2-contributor", contextWindow: 1048576, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "meta/muse-spark-1.3", contextWindow: 1048576, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "meta/muse-spark-1.3-contributor", contextWindow: 1048576, efforts: []string{"low", "medium", "high", "xhigh"}},
	{id: "nvidia/nemotron-3-ultra-550b-a55b", contextWindow: 1000000, efforts: nil},
	{id: "poolside/laguna-s-2.1-free", contextWindow: 256000, efforts: nil},
	{id: "inclusionai/ling-3.0-flash-sante:free", contextWindow: 262144, efforts: nil},
	{id: "inclusionai/ling-3.1-flash:free", contextWindow: 262144, efforts: []string{"low", "medium", "high"}},
	{id: "stealth/space-bunny-alpha", contextWindow: 1000000, efforts: []string{"low", "medium", "high"}},
	{id: "stealth/pixel-canary", contextWindow: 262144, efforts: []string{"low", "medium", "xhigh"}},
}

// Allocate fresh metadata because option projection can modify its input.
func gatewayModels() []*agent.ModelInfo {
	models := make([]*agent.ModelInfo, 0, len(nativeGatewayCatalog))
	for _, row := range nativeGatewayCatalog {
		model := &agent.ModelInfo{Id: row.id, DisplayName: row.id, ContextWindow: row.contextWindow, Description: "Requires Command Code service access"}
		for _, effort := range row.efforts {
			model.SupportedEfforts = append(model.SupportedEfforts, providerkit.EffortTier(effort))
		}
		providerkit.SortEffortsDescending(model.SupportedEfforts)
		models = append(models, model)
	}
	return models
}
