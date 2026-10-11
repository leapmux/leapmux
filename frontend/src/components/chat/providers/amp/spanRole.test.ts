import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { ampRelatedMessages, ampSpanRole } from './spanRole'
import { ampToolResultRow, ampToolUseRow } from './toolResults.fixtures'

function row(parent: Record<string, unknown>, completion?: MessageCompletion) {
  return { ...input(parent, undefined, AgentProvider.AMP), ...(completion !== undefined ? { completion } : {}) }
}

describe('ampSpanRole', () => {
  it('reads the side of a span from the block', () => {
    expect(ampSpanRole(row(ampToolUseRow('shell_command', { command: 'ls' })))).toBe('request')
    expect(ampSpanRole(row(ampToolResultRow('ok')))).toBe('result')
    expect(ampSpanRole(row({ type: 'assistant', message: { content: [{ type: 'text', text: 'x' }] } }))).toBe('other')
    expect(ampSpanRole(row({ type: 'result', subtype: 'success' }))).toBe('other')
  })

  it('reads a retained call row as the result', () => {
    expect(ampSpanRole(row(ampToolUseRow('shell_command', {}), MessageCompletion.INTERRUPTED))).toBe('result')
  })

  it('reads a block with no call id as no side', () => {
    expect(ampSpanRole(row({ type: 'assistant', message: { content: [{ type: 'tool_use', name: 'shell_command', input: {} }] } }))).toBe('other')
    expect(ampSpanRole(row({ type: 'user', message: { content: [{ type: 'tool_result', content: 'x' }] } }))).toBe('other')
  })
})

describe('ampRelatedMessages', () => {
  it('asks for the other side of the span', () => {
    expect(ampRelatedMessages(row(ampToolUseRow('shell_command', {})))).toEqual(['result'])
    expect(ampRelatedMessages(row(ampToolResultRow('ok')))).toEqual(['request'])
    expect(ampRelatedMessages(row({ type: 'result' }))).toEqual([])
  })
})
