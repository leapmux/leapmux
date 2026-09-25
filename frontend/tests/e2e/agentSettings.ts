// A RELATIVE import, not `~/...`. `tsconfig.json` lists only
// `tests/e2e/**/*.test.ts` under `include`, and vite's tsconfigPaths refuses the
// `~` mapping for an importer outside `include` -- see that file's own comment.
// This module is not a `.test.ts`, so `~/components/chat/settingsGroups` fails
// to resolve here although a sibling spec may use it.
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { ACCOUNT_DEFAULT_MODEL } from '../../src/generated/contracts/worker-vocab'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { KIRO_DEFAULT_MOCK_MODEL } from './helpers/kiroSurface'
import { KIMI_MOCK_MODELS, MOCK_MODELS, MOCK_PROVIDER_IDS, QWEN_MODEL_ID } from './helpers/mockAgentEnvironment'

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
  // `auto` is the alias of the mock catalogue's default variant, and the one
  // LeapMux normalizes to. See the note above.
  [AgentProvider.CURSOR]: { model: 'auto' },
  [AgentProvider.GOOSE]: { model: MOCK_MODELS.zai, effort: 'high' },
  [AgentProvider.KIMI_CODE]: { model: KIMI_MOCK_MODELS.thinking, effort: 'high' },
  [AgentProvider.KILO]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  // Kiro's catalogue comes from its own service, which `helpers/kiroSurface.ts`
  // answers. The effort differs from the model's own default, so a start that
  // failed to apply it shows.
  [AgentProvider.KIRO]: { model: KIRO_DEFAULT_MOCK_MODEL.modelId, effort: 'medium' },
  // MiMo reads the OpenCode family's provider block, and its effort is the model's
  // reasoning variant.
  [AgentProvider.MIMO_CODE]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.OPENCODE]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.PI]: { model: MOCK_MODELS.pi, effort: 'high' },
  [AgentProvider.GROK_BUILD]: { model: MOCK_MODELS.grok, effort: 'medium' },
  // Qwen states a model as `<id>(<auth type>)`.
  [AgentProvider.QWEN_CODE]: { model: QWEN_MODEL_ID, effort: 'high' },
  // omp addresses a model as `<provider>/<id>`, the provider of its own `models.yml`.
  [AgentProvider.OH_MY_PI]: { model: `${MOCK_PROVIDER_IDS.ohMyPi}/${MOCK_MODELS.ohMyPi}`, effort: 'high' },
  [AgentProvider.REASONIX]: { model: MOCK_MODELS.deepseek },
  // ZCode requires the provider-qualified identifier of its configured model.
  [AgentProvider.ZCODE]: { model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`, effort: 'high' },
  // A bare id: the model belongs to the provider that the isolated
  // configuration selects.
  [AgentProvider.CODEWHALE]: { model: MOCK_MODELS.deepseek },
  // Amp has no model axis and no effort axis: its agent mode chooses both, on its
  // own service. The account-default sentinel states that, and the worker sends no
  // model to Amp.
  [AgentProvider.AMP]: { model: ACCOUNT_DEFAULT_MODEL },
  // The model of the isolated `openai-compatible` settings. Cline's catalog states no
  // reasoning ladder for a model of that provider, so the model has no effort axis.
  [AgentProvider.CLINE]: { model: MOCK_MODELS.cline },
} as const satisfies Record<Exclude<AgentProvider, AgentProvider.UNSPECIFIED>, AgentE2ESettings>

/** The pinned settings of one provider. */
export function agentSettings(provider: AgentProvider): AgentE2ESettings {
  const settings = (AGENT_E2E_SETTINGS as Record<number, AgentE2ESettings | undefined>)[provider]
  if (!settings)
    throw new Error(`agentSettings: no pinned model for AgentProvider ${provider}`)
  return settings
}

/**
 * Builds the initial option map for one agent test fixture.
 *
 * The effort travels under the well-known `effort` id whatever the provider's
 * own axis is called: the worker maps it onto that axis at startup (see
 * `applyStartupOptions` and `startupEffortConfigID` in the Go worker).
 */
export function agentOpenOptions(settings: AgentE2ESettings) {
  return {
    model: settings.model,
    ...(settings.effort
      ? { optionValues: { [OPTION_ID_EFFORT]: settings.effort } }
      : {}),
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
