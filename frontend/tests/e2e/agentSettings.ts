// A RELATIVE import, not `~/...`. `tsconfig.json` lists only
// `tests/e2e/**/*.test.ts` under `include`, and vite's tsconfigPaths refuses the
// `~` mapping for an importer outside `include` -- see that file's own comment.
// This module is not a `.test.ts`, so `~/components/chat/settingsGroups` fails
// to resolve here although a sibling spec may use it.
import { OPTION_ID_EFFORT, OPTION_ID_MODEL } from '../../src/components/chat/settingsGroups'
import { ACCOUNT_DEFAULT_MODEL } from '../../src/generated/contracts/worker-vocab'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DEEPSEEK_HARNESS_MODEL_ID } from './helpers/deepseekHarnessEnvironment'
import { KIRO_DEFAULT_MOCK_MODEL } from './helpers/kiroSurface'
import { CODEBUDDY_MODEL_ID, COMMAND_CODE_MODEL_ID, DROID_MOCK_MODEL_IDS, FAST_AGENT_MOCK_MODEL, GEMINI_MODEL_ID, JUNIE_MOCK_MODEL, KIMI_MOCK_MODELS, LETTA_MODEL_ID, MOCK_MODELS, MOCK_PROVIDER_IDS, QODER_MODEL_ID, QWEN_MODEL_ID } from './helpers/mockAgentEnvironment'

/** The model, and the reasoning effort where the provider has one. */
export interface AgentE2ESettings {
  model: string
  /** Omitted for a provider whose models expose no effort axis. */
  effort?: string
}

/**
 * Concrete settings for every end-to-end agent fixture.
 *
 * EVERY provider reaches the mock model endpoint. Every provider but Cursor and
 * Kimi Code takes a model out of `MOCK_MODELS`, which the isolated agent
 * configuration writes. Kimi Code takes an alias out of `KIMI_MOCK_MODELS`,
 * because it addresses a model by the key of its configured model table rather
 * than by the model identifier. Cursor takes `auto`, because its model does not
 * come from a local runtime at all: the CLI asks its own backend for a
 * catalogue, and `helpers/cursorSurface.ts` answers that call with
 * `CURSOR_MOCK_MODELS`, whose default variant answers to `auto`.
 *
 * Keep this catalog explicit so an account default cannot change what a test
 * observes. `satisfies` makes a new provider a typecheck failure here rather
 * than a test that silently takes that default.
 */
export const AGENT_E2E_SETTINGS = {
  [AgentProvider.CLAUDE_CODE]: { model: MOCK_MODELS.anthropic, effort: 'medium' },
  [AgentProvider.CODEX]: { model: MOCK_MODELS.openai, effort: 'medium' },
  [AgentProvider.GITHUB_COPILOT]: { model: MOCK_MODELS.openai, effort: 'medium' },
  [AgentProvider.CURSOR]: { model: 'auto' },
  [AgentProvider.GOOSE]: { model: MOCK_MODELS.goose },
  [AgentProvider.KIMI_CODE]: { model: KIMI_MOCK_MODELS.thinking, effort: 'high' },
  [AgentProvider.KIRO]: { model: KIRO_DEFAULT_MOCK_MODEL.modelId, effort: 'medium' },
  [AgentProvider.PI]: { model: MOCK_MODELS.pi, effort: 'high' },
  [AgentProvider.GROK_BUILD]: { model: MOCK_MODELS.grok, effort: 'medium' },
  [AgentProvider.QWEN_CODE]: { model: QWEN_MODEL_ID, effort: 'high' },
  [AgentProvider.REASONIX]: { model: MOCK_MODELS.deepseek },
  [AgentProvider.CODEWHALE]: { model: MOCK_MODELS.deepseek },
  [AgentProvider.AMP]: { model: ACCOUNT_DEFAULT_MODEL },
  [AgentProvider.CLINE]: { model: MOCK_MODELS.cline },
  [AgentProvider.JUNIE]: { model: JUNIE_MOCK_MODEL },
  [AgentProvider.CODEBUDDY]: { model: CODEBUDDY_MODEL_ID },
  [AgentProvider.LETTA]: { model: LETTA_MODEL_ID },
  [AgentProvider.DIRAC]: { model: MOCK_MODELS.deepseek },
  [AgentProvider.DROID]: { model: DROID_MOCK_MODEL_IDS.primary, effort: 'none' },
  [AgentProvider.QODER]: { model: QODER_MODEL_ID },
  [AgentProvider.COMMAND_CODE]: { model: COMMAND_CODE_MODEL_ID, effort: 'high' },
  [AgentProvider.DEEPSEEK_HARNESS]: { model: DEEPSEEK_HARNESS_MODEL_ID, effort: 'high' },
  [AgentProvider.GEMINI_CLI]: { model: GEMINI_MODEL_ID },
  [AgentProvider.FAST_AGENT]: { model: FAST_AGENT_MOCK_MODEL },
  [AgentProvider.KILO]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.MIMO_CODE]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.OPENCODE]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.OH_MY_PI]: { model: `${MOCK_PROVIDER_IDS.ohMyPi}/${MOCK_MODELS.ohMyPi}`, effort: 'high' },
  [AgentProvider.ZCODE]: { model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`, effort: 'high' },
} as const satisfies Record<Exclude<AgentProvider, AgentProvider.UNSPECIFIED>, AgentE2ESettings>

/** The pinned settings of one provider. */
export function agentSettings(provider: AgentProvider): AgentE2ESettings {
  const settings = (AGENT_E2E_SETTINGS as Record<number, AgentE2ESettings | undefined>)[provider]
  if (!settings)
    throw new Error(`agentSettings: no pinned model for AgentProvider ${provider}`)
  return settings
}

/** What a test changes in the pinned settings of the agent that it opens. */
export interface AgentOpenOverrides {
  /** The model. The default is the pinned model of the provider. */
  model?: string
  /**
   * Option values over the pinned values. A value of the same option ID replaces the pinned one.
   * A model here throws: give it as `model`, the one place a model comes from.
   */
  optionValues?: Record<string, string>
}

/** The provider, the model, and the option values of one agent open request. */
export interface AgentOpenOptions {
  agentProvider: AgentProvider
  model: string
  optionValues: Record<string, string>
}

/**
 * Build the open request of one agent: the pinned settings of the provider, merged with the test's overrides.
 *
 * This is the one merge rule of the E2E suite. The model comes only from `overrides.model`, and the pinned model is
 * the default. The option values start from the pinned effort, and each override replaces the value of its own ID.
 * A spread of pinned settings followed by a second `optionValues` dropped the pinned effort, and a model given as an
 * option value lost to the pinned model, because the open request writes the model last. This function makes
 * both mistakes impossible.
 *
 * The effort travels under the well-known `effort` id whatever the provider's
 * own axis is called: the worker maps it onto that axis at startup (see
 * `applyStartupOptions` and `startupEffortConfigID` in the Go worker).
 */
export function agentOpenOptions(provider: AgentProvider, overrides: AgentOpenOverrides = {}): AgentOpenOptions {
  if (overrides.optionValues && Object.hasOwn(overrides.optionValues, OPTION_ID_MODEL))
    throw new Error('agentOpenOptions: give the model as `model`, not as an option value.')
  if (overrides.model !== undefined && overrides.model.trim() === '')
    throw new Error('agentOpenOptions: a model override needs a model ID.')
  const pinned = agentSettings(provider)
  return {
    agentProvider: provider,
    model: overrides.model ?? pinned.model,
    optionValues: {
      ...(pinned.effort ? { [OPTION_ID_EFFORT]: pinned.effort } : {}),
      ...overrides.optionValues,
    },
  }
}

/**
 * The `LEAPMUX_*_DEFAULT_*` pairs a spawned hub or worker needs, so the catalog
 * states the mapping once instead of at each `spawn` call.
 *
 * The pairs cover Claude Code, Codex and Copilot, the providers of the specs
 * that spawn their own hub or worker. Copilot's native protocol drives its
 * reasoning axis through the well-known effort id
 * (`session.model.setReasoningEffort`). Most other providers register such keys
 * also, but no spec spawns a process for them: their fixtures pin the settings
 * through the open request (`agentOpenOptions`).
 *
 * `LEAPMUX_WORKER_NAME` stays at each call site, because it differs by site.
 */
export function agentDefaultsEnv(): Record<string, string> {
  const claude = AGENT_E2E_SETTINGS[AgentProvider.CLAUDE_CODE]
  const codex = AGENT_E2E_SETTINGS[AgentProvider.CODEX]
  const copilot = AGENT_E2E_SETTINGS[AgentProvider.GITHUB_COPILOT]
  return {
    LEAPMUX_CLAUDE_DEFAULT_MODEL: claude.model,
    LEAPMUX_CLAUDE_DEFAULT_EFFORT: claude.effort,
    LEAPMUX_CODEX_DEFAULT_MODEL: codex.model,
    LEAPMUX_CODEX_DEFAULT_EFFORT: codex.effort,
    LEAPMUX_COPILOT_DEFAULT_MODEL: copilot.model,
    LEAPMUX_COPILOT_DEFAULT_EFFORT: copilot.effort,
  }
}
