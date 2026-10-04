import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { ampToolResultRow, ampToolUseRow } from '../toolResults.fixtures'
import { ampBlockText, ampMessageBlocks, ampSideCallId, ampToolResult, ampToolUse } from './toolCommon'

describe('ampToolUse', () => {
  it('reads the call of an assistant row', () => {
    expect(ampToolUse(ampToolUseRow('shell_command', { command: 'ls' }, 'TU-a'))).toEqual({ id: 'TU-a', name: 'shell_command', input: { command: 'ls' } })
  })

  it('answers null for a row that states no call', () => {
    expect(ampToolUse(ampToolResultRow('x'))).toBeNull()
    expect(ampToolUse({ type: 'assistant', message: { content: [{ type: 'tool_use', name: 'x' }] } })).toBeNull()
    expect(ampToolUse({ type: 'result' })).toBeNull()
    expect(ampToolUse(undefined)).toBeNull()
  })

  it('reads a call whose input is not an object as a call with no arguments', () => {
    expect(ampToolUse({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'TU-a', name: 'x', input: 'text' }] } })?.input).toEqual({})
  })
})

describe('ampToolResult', () => {
  it('reads a string result', () => {
    expect(ampToolResult(ampToolResultRow('pong', false, 'TU-a'))).toEqual({ toolUseId: 'TU-a', content: 'pong', isError: false })
    expect(ampToolResult(ampToolResultRow('boom', true, 'TU-a'))?.isError).toBe(true)
  })

  it('joins a result stated as text blocks', () => {
    const row = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'TU-a', content: [{ type: 'text', text: 'a' }, { type: 'image' }, { type: 'text', text: 'b' }] }] } }
    expect(ampToolResult(row)?.content).toBe('a\nb')
  })

  it('answers null for a row that states no result', () => {
    expect(ampToolResult(ampToolUseRow('x', {}))).toBeNull()
    expect(ampToolResult({ type: 'user', message: { content: [{ type: 'tool_result', content: 'x' }] } })).toBeNull()
  })
})

describe('ampMessageBlocks and ampBlockText', () => {
  it('read the blocks of a row and join the text of one kind', () => {
    const row = { type: 'assistant', message: { content: [{ type: 'text', text: 'a' }, 'junk', { type: 'thinking', thinking: 't' }, { type: 'text', text: ' ' }, { type: 'text', text: 'b' }] } }
    expect(ampMessageBlocks(row)).toHaveLength(4)
    expect(ampBlockText(row, 'text', 'text')).toBe('a\n\nb')
    expect(ampBlockText(row, 'thinking', 'thinking')).toBe('t')
    expect(ampMessageBlocks({ type: 'assistant', message: { content: 'text' } })).toEqual([])
  })
})

describe('ampSideCallId', () => {
  it('reads the call id of either side', () => {
    expect(ampSideCallId(input(ampToolUseRow('x', {}, 'TU-a'), undefined, AgentProvider.AMP))).toBe('TU-a')
    expect(ampSideCallId(input(ampToolResultRow('x', false, 'TU-b'), undefined, AgentProvider.AMP))).toBe('TU-b')
    expect(ampSideCallId(input({ type: 'result' }, undefined, AgentProvider.AMP))).toBe('')
    expect(ampSideCallId(undefined)).toBe('')
  })
})
