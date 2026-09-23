// A RELATIVE import, not `~/...`. `tsconfig.json` lists only
// `tests/e2e/**/*.test.ts` under `include`, and vite's tsconfigPaths refuses the
// `~` mapping for an importer outside `include` -- see that file's own comment.
// This module is not a `.test.ts`, so `~/components/chat/settingsGroups` fails
// to resolve here although a sibling spec may use it.
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from './helpers/mockAgentEnvironment'

/** The model, and the reasoning effort where the provider has one. */
export interface AgentE2ESettings {
  model: string
  /** Omitted for a provider whose models expose no effort axis. */
  effort?: string
}

/**
 * Concrete settings for every end-to-end agent fixture.
 *
 * EVERY provider reaches the mock model endpoint. Nine take a model out of
 * `MOCK_MODELS`, which the isolated agent configuration writes. Cursor takes
 * `auto`, because its model does not come from a local runtime at all: the CLI
 * asks its own backend for a catalogue, and `helpers/cursorSurface.ts` answers
 * that call with `CURSOR_MOCK_MODELS`, whose default variant answers to `auto`.
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
  [AgentProvider.KILO]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.OPENCODE]: { model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`, effort: 'high' },
  [AgentProvider.PI]: { model: MOCK_MODELS.pi, effort: 'high' },
  [AgentProvider.REASONIX]: { model: MOCK_MODELS.deepseek },
  // ZCode requires the provider-qualified identifier of its configured model.
  [AgentProvider.ZCODE]: { model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`, effort: 'high' },
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
 * Claude Code, Codex and Copilot each read a model and an effort: Copilot's
 * native protocol drives its reasoning axis through the well-known effort id
 * (`session.model.setReasoningEffort`). The other Agent Client Protocol providers
 * register neither key, so their fixtures pin the settings through the open
 * request (`agentOpenOptions`).
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
