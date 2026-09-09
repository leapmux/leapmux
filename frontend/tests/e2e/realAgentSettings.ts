// A RELATIVE import, not `~/...`. `tsconfig.json` lists only
// `tests/e2e/**/*.test.ts` under `include`, and vite's tsconfigPaths refuses the
// `~` mapping for an importer outside `include` -- see that file's own comment.
// This module is not a `.test.ts`, so `~/components/chat/settingsGroups` fails
// to resolve here although a sibling spec may use it.
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'

/** The model, and the reasoning effort where the provider has one. */
export interface RealAgentSettings {
  model: string
  /** Omitted for a provider whose models expose no effort axis. */
  effort?: string
}

/**
 * Concrete settings for end-to-end tests that contact a real model provider.
 * Keep this catalog explicit so account defaults cannot change test behavior.
 *
 * Keyed by `AgentProvider`, and `satisfies` makes a new provider a typecheck
 * failure here rather than a test that silently takes the account default.
 */
export const REAL_AGENT_E2E_SETTINGS = {
  [AgentProvider.CLAUDE_CODE]: { model: 'sonnet', effort: 'medium' },
  [AgentProvider.CODEX]: { model: 'gpt-5.6-luna', effort: 'medium' },
  [AgentProvider.GITHUB_COPILOT]: { model: 'gpt-5.6-luna', effort: 'medium' },
  [AgentProvider.CURSOR]: { model: 'auto' },
  [AgentProvider.GOOSE]: { model: 'glm-5.3-flash', effort: 'high' },
  // Kilo's default image model does not run agentic turns. Keep a text-capable
  // model so subagent spawns run.
  [AgentProvider.KILO]: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
  [AgentProvider.OPENCODE]: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
  [AgentProvider.PI]: { model: 'glm-5.3-flash', effort: 'high' },
  [AgentProvider.REASONIX]: { model: 'deepseek-flash' },
  // ZCode requires the configured model's exact case and provider-qualified id.
  [AgentProvider.ZCODE]: { model: 'builtin:zai-coding-plan/GLM-5.3-Flash', effort: 'high' },
} as const satisfies Record<Exclude<AgentProvider, AgentProvider.UNSPECIFIED>, RealAgentSettings>

/** The pinned settings of one provider. */
export function realAgentSettings(provider: AgentProvider): RealAgentSettings {
  const settings = (REAL_AGENT_E2E_SETTINGS as Record<number, RealAgentSettings | undefined>)[provider]
  if (!settings)
    throw new Error(`realAgentSettings: no pinned model for AgentProvider ${provider}`)
  return settings
}

/**
 * Builds the initial option map for one real-agent test fixture.
 *
 * The effort travels under the well-known `effort` id whatever the provider's
 * own axis is called: the worker maps it onto that axis at startup (see
 * `applyStartupOptions` and `startupEffortConfigID` in the Go worker).
 */
export function realAgentOpenOptions(settings: RealAgentSettings) {
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
 * Claude Code and Codex own a static model catalog, so they read both a model
 * and an effort. Copilot reads a model alone: its reasoning axis is the
 * daemon's `reasoning_effort` config option, and it registers no env effort key
 * (see `copilot.go`). The other ACP providers register neither, so their
 * fixtures pin the settings through the open request (`realAgentOpenOptions`).
 *
 * `LEAPMUX_WORKER_NAME` stays at each call site, because it differs by site.
 */
export function realAgentEnv(): Record<string, string> {
  const claude = REAL_AGENT_E2E_SETTINGS[AgentProvider.CLAUDE_CODE]
  const codex = REAL_AGENT_E2E_SETTINGS[AgentProvider.CODEX]
  return {
    LEAPMUX_CLAUDE_DEFAULT_MODEL: claude.model,
    LEAPMUX_CLAUDE_DEFAULT_EFFORT: claude.effort,
    LEAPMUX_CODEX_DEFAULT_MODEL: codex.model,
    LEAPMUX_CODEX_DEFAULT_EFFORT: codex.effort,
    LEAPMUX_COPILOT_DEFAULT_MODEL: REAL_AGENT_E2E_SETTINGS[AgentProvider.GITHUB_COPILOT].model,
  }
}
