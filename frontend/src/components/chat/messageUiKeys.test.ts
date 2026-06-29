import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
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

  it('routes thinking to CODEX_REASONING for Codex and THINKING otherwise', () => {
    // Codex reasoning renders under its OWN key (reasoning.tsx), not the shared
    // THINKING key Claude/Pi/ACP thinking uses -- so the estimator and the renderer
    // must agree via this single mapper.
    expect(expandedUiKeyFor('assistant_thinking', AgentProvider.CODEX)).toBe(MESSAGE_UI_KEY.CODEX_REASONING)
    expect(expandedUiKeyFor('assistant_thinking', AgentProvider.CLAUDE_CODE)).toBe(MESSAGE_UI_KEY.THINKING)
    expect(expandedUiKeyFor('assistant_thinking', undefined)).toBe(MESSAGE_UI_KEY.THINKING)
  })

  it('returns a harmless THINKING default for non-expand kinds (the value is unused for them)', () => {
    // tool_result/assistant_text rows never read the expand key, but the mapper is
    // total -- it must not throw, and Codex still routes to its reasoning key.
    expect(expandedUiKeyFor('tool_result', AgentProvider.CLAUDE_CODE)).toBe(MESSAGE_UI_KEY.THINKING)
    expect(expandedUiKeyFor('assistant_text', AgentProvider.CODEX)).toBe(MESSAGE_UI_KEY.CODEX_REASONING)
  })
})
