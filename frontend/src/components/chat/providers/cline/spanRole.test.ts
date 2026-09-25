import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../registry'
import { clineRelatedMessages, clineSpanRole } from './spanRole'
import { clineToolFinishRow, clineToolStartRow } from './toolResults.fixtures'
import '~/components/chat/providers'

function resolved(parent: Record<string, unknown>, completion?: MessageCompletion) {
  return resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null, ...(completion !== undefined ? { completion } : {}) }, AgentProvider.CLINE)
}

describe('clineSpanRole', () => {
  it('reads a start as the request and a finish as the result', () => {
    expect(clineSpanRole(resolved(clineToolStartRow('read_files', {})))).toBe('request')
    expect(clineSpanRole(resolved(clineToolFinishRow('read_files', [])))).toBe('result')
  })

  it('reads a retained start as the result', () => {
    expect(clineSpanRole(resolved(clineToolStartRow('read_files', {}), MessageCompletion.INTERRUPTED))).toBe('result')
  })

  it('reads every other row as no side', () => {
    expect(clineSpanRole(resolved({ version: 'v1', event: 'assistant.finished', payload: { text: 'x' } }))).toBe('other')
  })

  // A row with no call id states no call, so no span can hold it.
  it('reads a tool row with no call id as no side', () => {
    expect(clineSpanRole(resolved({ version: 'v1', event: 'tool.started', payload: { toolName: 'read_files' } }))).toBe('other')
    expect(clineSpanRole(resolved({ version: 'v1', event: 'tool.finished', payload: { toolName: 'read_files', output: [] } }))).toBe('other')
    expect(clineSpanRole(resolved({ version: 'v1', event: 'tool.started', payload: { toolName: 'read_files' } }, MessageCompletion.INTERRUPTED))).toBe('other')
  })
})

describe('clineRelatedMessages', () => {
  it('pairs each side with the other', () => {
    expect(clineRelatedMessages(resolved(clineToolStartRow('read_files', {})))).toEqual(['result'])
    expect(clineRelatedMessages(resolved(clineToolFinishRow('read_files', [])))).toEqual(['request'])
    expect(clineRelatedMessages(resolved({ content: 'hello' }))).toEqual([])
  })
})
