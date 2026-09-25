import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { resolveOhMyPiMessage } from './resolveMessage'

const startFrame = { type: 'tool_execution_start', toolCallId: 'call_1', toolName: 'bash', args: { command: 'sleep 60' } }
const partial = { content: [{ type: 'text', text: 'so far' }], details: {} }

function parsed(parentObject: Record<string, unknown> | undefined, supplementalContent: unknown): ParsedMessageContent {
  return { wrapper: null, topLevel: parentObject ?? null, parentObject, rawText: '', supplementalContent, messageMetadata: undefined }
}

describe('resolveOhMyPiMessage', () => {
  it('puts the partial result on the start frame', () => {
    expect(resolveOhMyPiMessage(parsed(startFrame, { toolCallId: 'call_1', toolName: 'bash', partialResult: partial }))).toEqual({ ...startFrame, result: partial })
  })

  it('returns the same frame when it is already resolved', () => {
    const resolved = { ...startFrame, result: partial }
    expect(resolveOhMyPiMessage(parsed(resolved, { toolCallId: 'call_1', toolName: 'bash', partialResult: partial }))).toBe(resolved)
  })

  it('leaves the frame alone for a supplement of another call, another tool, or no result', () => {
    for (const supplement of [
      { toolCallId: 'call_2', toolName: 'bash', partialResult: partial },
      { toolCallId: 'call_1', toolName: 'read', partialResult: partial },
      { toolCallId: 'call_1', toolName: 'bash' },
      'not an object',
      undefined,
    ])
      expect(resolveOhMyPiMessage(parsed(startFrame, supplement))).toBe(startFrame)
  })

  it('leaves an end frame and a frame with no call id alone', () => {
    const endFrame = { type: 'tool_execution_end', toolCallId: 'call_1', toolName: 'bash', result: {} }
    expect(resolveOhMyPiMessage(parsed(endFrame, { toolCallId: 'call_1', toolName: 'bash', partialResult: partial }))).toBe(endFrame)
    const noId = { type: 'tool_execution_start', toolName: 'bash' }
    expect(resolveOhMyPiMessage(parsed(noId, { toolCallId: '', toolName: 'bash', partialResult: partial }))).toBe(noId)
    expect(resolveOhMyPiMessage(parsed(undefined, {}))).toBeUndefined()
  })

  it('leaves a frame with no tool name alone, although the supplement states the same empty name', () => {
    // An empty name matches an empty name, so the identity test alone would join them.
    const noName = { type: 'tool_execution_start', toolCallId: 'call_1' }
    expect(resolveOhMyPiMessage(parsed(noName, { toolCallId: 'call_1', toolName: '', partialResult: partial }))).toBe(noName)
  })

  it('leaves the frame alone for a partial result that is not a record', () => {
    for (const partialResult of ['so far', null, ['so far']])
      expect(resolveOhMyPiMessage(parsed(startFrame, { toolCallId: 'call_1', toolName: 'bash', partialResult })), JSON.stringify(partialResult)).toBe(startFrame)
  })

  it('leaves the stored frame unchanged when it resolves one', () => {
    const stored = { ...startFrame }
    const resolved = resolveOhMyPiMessage(parsed(stored, { toolCallId: 'call_1', toolName: 'bash', partialResult: partial }))
    expect(resolved).not.toBe(stored)
    expect(stored).toEqual(startFrame)
  })
})
