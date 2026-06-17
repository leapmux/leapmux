import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { expandedUiKeyFor, MESSAGE_UI_DEFAULTS, MESSAGE_UI_KEY, messageUiDefault, toolBodyExpandedKeyFor, uiFlagsConsumedBy } from './messageUiKeys'

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

describe('uiflagsconsumedby', () => {
  it('marks tool_result as collapse-consuming only', () => {
    expect(uiFlagsConsumedBy('tool_result')).toEqual({ collapsed: true, expanded: false, toolBodyExpanded: false })
  })

  it('marks the expandable bubble kinds as expand-consuming only', () => {
    for (const kind of ['assistant_thinking', 'plan_execution', 'agent_prompt']) {
      expect(uiFlagsConsumedBy(kind)).toEqual({ collapsed: false, expanded: true, toolBodyExpanded: false })
    }
  })

  it('marks a Bash OR ACP execute tool_use as toolBodyExpanded-consuming, by tool name', () => {
    // The estimator passes the `tool_use:<name>` prefixed kind; the bare kind never
    // matches the expanded set, so only the toolName checks fire. A Bash command
    // summary has no result body (collapsed false); an ACP `execute` ALSO renders a
    // collapsible result body (its output reads TOOL_RESULT_EXPANDED), so it consumes
    // both flags.
    expect(uiFlagsConsumedBy('tool_use:Bash', 'Bash')).toEqual({ collapsed: false, expanded: false, toolBodyExpanded: true })
    expect(uiFlagsConsumedBy('tool_use:execute', 'execute')).toEqual({ collapsed: true, expanded: false, toolBodyExpanded: true })
    expect(uiFlagsConsumedBy('tool_use:Read', 'Read')).toEqual({ collapsed: false, expanded: false, toolBodyExpanded: false })
  })

  it('marks Codex/ACP result-body tool rows as collapse-consuming, by tool name', () => {
    // commandExecution (settled -> ToolResultMessage) and the ACP read/search/fetch
    // bodies collapse on TOOL_RESULT_EXPANDED; a collab prompt on its own key. Codex
    // webSearch (header-only in its settled form) consumes nothing here.
    for (const tool of ['commandExecution', 'collabAgentToolCall', 'read', 'search', 'fetch']) {
      expect(uiFlagsConsumedBy('tool_use', tool).collapsed).toBe(true)
    }
    expect(uiFlagsConsumedBy('tool_use', 'webSearch').collapsed).toBe(false)
  })

  it('consumes no flags for a plain row (assistant_text / user_content)', () => {
    for (const kind of ['assistant_text', 'user_content', 'notification']) {
      expect(uiFlagsConsumedBy(kind)).toEqual({ collapsed: false, expanded: false, toolBodyExpanded: false })
    }
  })
})

describe('toolbodyexpandedkeyfor', () => {
  it('maps each provider command tool to the key its renderer toggles', () => {
    // Claude/Pi Bash -> TOOL_USE_LAYOUT; ACP execute -> OPENCODE_TOOL_CALL_UPDATE.
    expect(toolBodyExpandedKeyFor('Bash')).toBe(MESSAGE_UI_KEY.TOOL_USE_LAYOUT)
    expect(toolBodyExpandedKeyFor('execute')).toBe(MESSAGE_UI_KEY.OPENCODE_TOOL_CALL_UPDATE)
  })

  it('returns undefined for a tool with no full-command/body toggle', () => {
    expect(toolBodyExpandedKeyFor('Read')).toBeUndefined()
    expect(toolBodyExpandedKeyFor('commandExecution')).toBeUndefined() // Codex renders a 1-line summary
    expect(toolBodyExpandedKeyFor(undefined)).toBeUndefined()
  })
})
