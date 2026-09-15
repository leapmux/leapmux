import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { expandedUiKeyFor, MESSAGE_UI_DEFAULTS, MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'

describe('message_ui_defaults', () => {
  it('has a default entry for every MESSAGE_UI_KEY', () => {
    const keys = Object.values(MESSAGE_UI_KEY)
    for (const key of keys)
      expect(MESSAGE_UI_DEFAULTS[key], `missing default for ${key}`).toBeTypeOf('function')
    // No stale entries: the table has exactly the registered keys.
    expect(Object.keys(MESSAGE_UI_DEFAULTS).sort()).toEqual([...keys].sort())
  })

  it('expands thinking and codex reasoning per the expandAgentThoughts pref', () => {
    for (const key of [MESSAGE_UI_KEY.THINKING, MESSAGE_UI_KEY.CODEX_REASONING]) {
      expect(messageUiDefault(key, { expandAgentThoughts: true })).toBe(true)
      expect(messageUiDefault(key, { expandAgentThoughts: false })).toBe(false)
      // Unknown pref (no context): thinking bubbles default expanded.
      expect(messageUiDefault(key)).toBe(true)
      expect(messageUiDefault(key, {})).toBe(true)
    }
  })

  it('defaults every non-thinking key collapsed regardless of the pref', () => {
    const thinkingKeys = new Set<string>([MESSAGE_UI_KEY.THINKING, MESSAGE_UI_KEY.CODEX_REASONING])
    for (const key of Object.values(MESSAGE_UI_KEY)) {
      if (thinkingKeys.has(key))
        continue
      expect(messageUiDefault(key, { expandAgentThoughts: true }), `${key} should default collapsed`).toBe(false)
      expect(messageUiDefault(key, { expandAgentThoughts: false })).toBe(false)
      expect(messageUiDefault(key)).toBe(false)
    }
  })
})

describe('expandeduikeyfor', () => {
  it('maps plan_execution and agent_prompt by kind, regardless of provider', () => {
    for (const provider of [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, undefined]) {
      expect(expandedUiKeyFor('plan_execution', provider)).toBe(MESSAGE_UI_KEY.PLAN_EXECUTION)
      expect(expandedUiKeyFor('agent_prompt', provider)).toBe(MESSAGE_UI_KEY.AGENT_PROMPT)
    }
  })

  // No plugin is registered in this project, so every kind below takes the shared key.
  // Codex's own key is asserted where its hook lives: providers/codex/plugin.test.ts.
  it('takes the shared THINKING key when no plugin claims the kind', () => {
    expect(expandedUiKeyFor('assistant_thinking', AgentProvider.CLAUDE_CODE)).toBe(MESSAGE_UI_KEY.THINKING)
    expect(expandedUiKeyFor('assistant_thinking', undefined)).toBe(MESSAGE_UI_KEY.THINKING)
  })

  it('returns a harmless THINKING default for non-expand kinds (the value is unused for them)', () => {
    // tool_result/assistant_text rows never read the expand key, but the mapper is
    // total -- it must not throw.
    expect(expandedUiKeyFor('tool_result', AgentProvider.CLAUDE_CODE)).toBe(MESSAGE_UI_KEY.THINKING)
    expect(expandedUiKeyFor('assistant_text', AgentProvider.CLAUDE_CODE)).toBe(MESSAGE_UI_KEY.THINKING)
  })
})
