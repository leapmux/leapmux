import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'

/**
 * Concrete settings for end-to-end tests that contact a real model provider.
 * Keep this catalog explicit so account defaults cannot change test behavior.
 */
export const REAL_AGENT_E2E_SETTINGS = {
  claudeCode: { model: 'sonnet', effort: 'medium' },
  codex: { model: 'gpt-5.6-luna', effort: 'medium' },
  copilot: { model: 'gpt-5.6-luna', effort: 'medium' },
  cursor: { model: 'auto' },
  goose: { model: 'glm-5.3-flash', effort: 'high' },
  kilo: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
  opencode: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
  pi: { model: 'glm-5.3-flash', effort: 'high' },
  reasonix: { model: 'deepseek-flash' },
  // ZCode requires the configured model's exact case and provider-qualified id.
  zcode: { model: 'builtin:zai-coding-plan/GLM-5.3-Flash', effort: 'high' },
} as const

/** Builds the initial option map for one real-agent test fixture. */
export function realAgentOpenOptions(settings: { model: string, effort?: string }) {
  return {
    model: settings.model,
    ...(settings.effort
      ? { optionValues: { [OPTION_ID_EFFORT]: settings.effort } }
      : {}),
  }
}
