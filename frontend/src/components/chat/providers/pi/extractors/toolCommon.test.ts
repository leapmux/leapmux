import { describe, expect, it } from 'vitest'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { piExtractTool } from './toolCommon'

describe('piExtractTool', () => {
  it('reads each field of a tool frame', () => {
    expect(piExtractTool({
      type: PI_EVENT.ToolExecutionUpdate,
      toolCallId: 'call_1',
      toolName: 'bash',
      args: { command: 'ls' },
      partialResult: {
        content: [
          { type: 'text', text: 'a\n' },
        ],
        details: { x: 1 },
      },
    })).toStrictEqual({
      toolCallId: 'call_1',
      toolName: 'bash',
      args: { command: 'ls' },
      partialResult: {
        text: 'a\n',
        details: { x: 1 },
      },
      isError: false,
    })
  })

  it('reads the result and the error flag of an end frame, which carries no arguments', () => {
    expect(piExtractTool({
      type: PI_EVENT.ToolExecutionEnd,
      toolCallId: 'call_1',
      toolName: 'bash',
      result: {
        content: [
          { type: 'text', text: 'denied' },
        ],
      },
      isError: true,
    })).toStrictEqual({
      toolCallId: 'call_1',
      toolName: 'bash',
      args: {},
      result: {
        text: 'denied',
        details: {},
      },
      isError: true,
    })
  })

  it.each([
    [
      'no call id',
      { toolName: 'bash' },
    ],
    [
      'an empty call id',
      { toolCallId: '', toolName: 'bash' },
    ],
    [
      'no tool name',
      { toolCallId: 'call_1' },
    ],
  ])('answers null for a frame with %s', (_label, payload) => {
    expect(piExtractTool({ type: PI_EVENT.ToolExecutionStart, ...payload })).toBeNull()
  })

  it('answers null for an absent frame', () => {
    expect(piExtractTool(null)).toBeNull()
    expect(piExtractTool(undefined)).toBeNull()
  })

  // A row build reads one payload through the kind dispatch and again through each
  // per-tool extractor, so every read after the first answers the first object.
  it('answers the same object for the same frame', () => {
    const frame = {
      type: PI_EVENT.ToolExecutionStart,
      toolCallId: 'call_1',
      toolName: 'read',
      args: { path: '/a.ts' },
    }
    const first = piExtractTool(frame)

    expect(first).not.toBeNull()
    expect(piExtractTool(frame)).toBe(first)
    // A copy of the frame is another frame, and it gets an object of its own.
    expect(piExtractTool({ ...frame })).not.toBe(first)
    expect(piExtractTool({ ...frame })).toStrictEqual(first)
  })

  it('reads a frame that states no call once, and answers null each time', () => {
    let idReads = 0
    const frame = {
      type: PI_EVENT.ToolExecutionStart,
      get toolCallId(): string {
        idReads++
        return 'call_1'
      },
    }

    expect(piExtractTool(frame)).toBeNull()
    expect(piExtractTool(frame)).toBeNull()
    expect(idReads).toBe(1)
  })
})
