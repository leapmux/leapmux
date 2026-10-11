import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { describe, expect, it } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { piMcpResultFromSnapshot } from './mcpResult'

function snapshot(frames: unknown[]): NativeMessageSnapshot {
  return { agentId: 'actual-agent', agentSessionId: 'actual-session', messages: frames.map((frame, index) => makeMessage({ id: String(index), spanId: 'call', spanType: 'mcp__probe__inspect', content: rawContent(frame) })) }
}
const complete = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'mcp__probe__inspect', isError: false, result: { content: [], structuredContent: { structuredContent: { count: 0, enabled: false, text: '' } } } }

describe('piMcpResultFromSnapshot', () => {
  it('returns the exact completed native fields without flattening zero and empty values', () => {
    expect(piMcpResultFromSnapshot(snapshot([complete]), 'call', 'mcp__probe__inspect')).toEqual({ result: complete.result, failed: false })
  })

  it('preserves actual failure status', () => {
    expect(piMcpResultFromSnapshot(snapshot([{ ...complete, isError: true }]), 'call', 'mcp__probe__inspect').failed).toBe(true)
  })

  it.each([
    { label: 'absent', frames: [] },
    { label: 'duplicate', frames: [complete, complete] },
    { label: 'wrong call', frames: [{ ...complete, toolCallId: 'other' }] },
    { label: 'wrong tool', frames: [{ ...complete, toolName: 'other' }] },
    { label: 'progress only', frames: [{ ...complete, type: 'tool_execution_update' }] },
  ])('refuses $label native completion', ({ frames }) => {
    expect(() => piMcpResultFromSnapshot(snapshot(frames), 'call', 'mcp__probe__inspect')).toThrow('completed results')
  })

  it.each([{ ...complete, isError: undefined }, { ...complete, result: null }])('refuses missing result or status fields: %j', (frame) => {
    expect(() => piMcpResultFromSnapshot(snapshot([frame]), 'call', 'mcp__probe__inspect')).toThrow('failure status')
  })
})
