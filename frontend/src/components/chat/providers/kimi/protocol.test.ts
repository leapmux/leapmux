import { describe, expect, it } from 'vitest'
import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { kimiFrame } from '~/test-support/kimiFixtures'
import { kimiDisplay, kimiEvent, kimiEventData } from './protocol'

describe('kimiEvent', () => {
  it('reads a persisted event row', () => {
    const row = kimiFrame(KIMI_EVENT.TurnEnded, { reason: 'completed' }, 'agent-0')
    expect(kimiEvent(row)).toStrictEqual({ type: KIMI_EVENT.TurnEnded, agentId: 'agent-0', data: row })
  })

  it('takes the main agent for a row that states no agent', () => {
    expect(kimiEvent({ type: KIMI_EVENT.TurnEnded })?.agentId).toBe('main')
    expect(kimiEvent({ type: KIMI_EVENT.TurnEnded, agentId: '' })?.agentId).toBe('main')
    expect(kimiEvent({ type: KIMI_EVENT.TurnEnded, agentId: 7 })?.agentId).toBe('main')
  })

  it('reads a row whose type is not a string as no event', () => {
    expect(kimiEvent({ type: 7 })).toBeNull()
    expect(kimiEvent({ type: '' })).toBeNull()
  })

  it('reads no row that is not a Kimi event', () => {
    for (const row of [null, 'text', [], {}, { type: 'assistant' }, { type: 'settings_changed' }, { content: 'hi' }])
      expect(kimiEvent(row), JSON.stringify(row)).toBeNull()
  })
})

describe('kimiEventData', () => {
  it('returns the row of the asked type alone', () => {
    const row = kimiFrame(KIMI_EVENT.ToolResult, { toolCallId: 'c' })
    expect(kimiEventData(row, KIMI_EVENT.ToolResult)).toBe(row)
    expect(kimiEventData(row, KIMI_EVENT.ToolCallStarted)).toBeNull()
    expect(kimiEventData(undefined, KIMI_EVENT.ToolResult)).toBeNull()
    expect(kimiEventData({ type: 'not.an.event' }, 'not.an.event')).toBeNull()
  })
})

describe('kimiDisplay', () => {
  it('reads a call display and an approval display', () => {
    expect(kimiDisplay({ display: { kind: 'command' } })).toStrictEqual({ kind: 'command' })
    expect(kimiDisplay({ tool_input_display: { kind: 'plan_review' } }, 'tool_input_display')).toStrictEqual({ kind: 'plan_review' })
    expect(kimiDisplay({ display: 'command' })).toBeUndefined()
    expect(kimiDisplay(null)).toBeUndefined()
  })
})
