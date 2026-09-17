import { describe, expect, it } from 'vitest'
import { expandedUiKeyFor, MESSAGE_UI_DEFAULTS, MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'

describe('MESSAGE_UI_DEFAULTS', () => {
  it('has a default entry for every MESSAGE_UI_KEY', () => {
    const keys = Object.values(MESSAGE_UI_KEY)
    for (const key of keys)
      expect(MESSAGE_UI_DEFAULTS[key], `missing default for ${key}`).toBeTypeOf('function')
    // No stale entries: the table has exactly the registered keys.
    expect(Object.keys(MESSAGE_UI_DEFAULTS).sort()).toEqual([...keys].sort())
  })

  it('expands the thinking bubble per the expandAgentThoughts pref', () => {
    const key = MESSAGE_UI_KEY.THINKING
    expect(messageUiDefault(key, { expandAgentThoughts: true })).toBe(true)
    expect(messageUiDefault(key, { expandAgentThoughts: false })).toBe(false)
    // Unknown pref (no context): thinking bubbles default expanded.
    expect(messageUiDefault(key)).toBe(true)
    expect(messageUiDefault(key, {})).toBe(true)
  })

  it('defaults every non-thinking key collapsed regardless of the pref', () => {
    for (const key of Object.values(MESSAGE_UI_KEY)) {
      if (key === MESSAGE_UI_KEY.THINKING)
        continue
      expect(messageUiDefault(key, { expandAgentThoughts: true }), `${key} should default collapsed`).toBe(false)
      expect(messageUiDefault(key, { expandAgentThoughts: false })).toBe(false)
      expect(messageUiDefault(key)).toBe(false)
    }
  })

  // Every key is kind-scoped and provider-neutral. Three keys used to belong to Codex
  // alone, because it drew its own reasoning, command and web-search bubbles; those
  // rows draw through the shared components now.
  it('registers no provider-scoped key', () => {
    for (const key of Object.values(MESSAGE_UI_KEY))
      expect(key, `${key} identifies a provider`).not.toMatch(/^(?:codex|claude|pi|zcode|copilot|cursor|goose|kilo|opencode|reasonix)-/)
  })
})

describe('expandedUiKeyFor', () => {
  it('maps plan_execution and agent_prompt by kind', () => {
    expect(expandedUiKeyFor('plan_execution')).toBe(MESSAGE_UI_KEY.PLAN_EXECUTION)
    expect(expandedUiKeyFor('agent_prompt')).toBe(MESSAGE_UI_KEY.AGENT_PROMPT)
  })

  // Every provider's thinking row draws through the shared bubble, so one key serves
  // them all -- and the estimator no longer has to ask which provider a row came from.
  it('takes the shared THINKING key for a thinking row', () => {
    expect(expandedUiKeyFor('assistant_thinking')).toBe(MESSAGE_UI_KEY.THINKING)
  })

  it('returns a harmless THINKING default for non-expand kinds (the value is unused for them)', () => {
    // tool_result/assistant_text rows never read the expand key, but the mapper is
    // total -- it must not throw.
    expect(expandedUiKeyFor('tool_result')).toBe(MESSAGE_UI_KEY.THINKING)
    expect(expandedUiKeyFor('assistant_text')).toBe(MESSAGE_UI_KEY.THINKING)
  })
})
