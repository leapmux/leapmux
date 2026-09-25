import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { ohMyPiExtractTool, ohMyPiPairedRequest, ohMyPiPairedResult, ohMyPiToolResultText } from './toolCommon'

const provider = AgentProvider.OH_MY_PI

describe('ohMyPiExtractTool', () => {
  it('reads each field of a tool frame', () => {
    expect(ohMyPiExtractTool({
      type: 'tool_execution_update',
      toolCallId: 'call_1',
      toolName: 'bash',
      args: { command: 'ls' },
      intent: 'list the files',
      partialResult: { content: [{ type: 'text', text: 'a\n' }], details: { x: 1 } },
    })).toEqual({
      toolCallId: 'call_1',
      toolName: 'bash',
      args: { command: 'ls' },
      intent: 'list the files',
      partialResult: { text: 'a\n', details: { x: 1 } },
      isError: false,
    })
  })

  it('answers the same object for the same frame', () => {
    const frame = { type: 'tool_execution_end', toolCallId: 'c', toolName: 'read', result: {}, isError: true }
    expect(ohMyPiExtractTool(frame)).toBe(ohMyPiExtractTool(frame))
    expect(ohMyPiExtractTool(frame)?.isError).toBe(true)
  })

  it('refuses a frame with no call id or no tool name', () => {
    expect(ohMyPiExtractTool({ toolName: 'bash' })).toBeNull()
    expect(ohMyPiExtractTool({ toolCallId: 'c' })).toBeNull()
    expect(ohMyPiExtractTool({ toolCallId: '', toolName: 'bash' })).toBeNull()
    expect(ohMyPiExtractTool({ toolCallId: 7, toolName: 'bash' })).toBeNull()
    expect(ohMyPiExtractTool(null)).toBeNull()
  })

  it('reads the final and the partial result of one frame, and no arguments as none', () => {
    expect(ohMyPiExtractTool({
      type: 'tool_execution_start',
      toolCallId: 'c',
      toolName: 'bash',
      result: { content: [{ type: 'text', text: 'done' }] },
      partialResult: { content: [{ type: 'text', text: 'so far' }], details: 'not a record' },
    })).toEqual({
      toolCallId: 'c',
      toolName: 'bash',
      args: {},
      intent: '',
      result: { text: 'done', details: {} },
      partialResult: { text: 'so far', details: {} },
      isError: false,
    })
  })

  it('reads an error flag only when omp states `true`', () => {
    // A frame that states the flag in another type states no error.
    for (const isError of ['true', 1, null])
      expect(ohMyPiExtractTool({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'read', result: {}, isError })?.isError, String(isError)).toBe(false)
  })
})

describe('ohMyPiToolResultText', () => {
  it('joins the text blocks and leaves the images out', () => {
    expect(ohMyPiToolResultText({ content: [{ type: 'text', text: 'one' }, { type: 'image', data: 'x', mimeType: 'image/png' }, { type: 'text', text: 'two' }] })).toBe('one\n\ntwo')
    expect(ohMyPiToolResultText(undefined)).toBe('')
  })
})

describe('ohMyPiPairedRequest', () => {
  const end = { type: 'tool_execution_end', toolCallId: 'c', toolName: 'read', result: {} }

  it('accepts the start frame of the same call and tool', () => {
    const start = input({ type: 'tool_execution_start', toolCallId: 'c', toolName: 'read', args: {} }, undefined, provider)
    expect(ohMyPiPairedRequest(end, start)).toBe(start)
  })

  it('refuses another call, another tool, or a frame that is not a start', () => {
    expect(ohMyPiPairedRequest(end, input({ type: 'tool_execution_start', toolCallId: 'd', toolName: 'read' }, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedRequest(end, input({ type: 'tool_execution_start', toolCallId: 'c', toolName: 'bash' }, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedRequest(end, input(end, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedRequest(end, undefined)).toBeUndefined()
  })
})

describe('ohMyPiPairedResult', () => {
  const start = { type: 'tool_execution_start', toolCallId: 'c', toolName: 'read', args: {} }

  it('accepts the end frame of the same call and tool alone', () => {
    const end = input({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'read', result: {} }, undefined, provider)
    expect(ohMyPiPairedResult(start, end)).toBe(end)
    expect(ohMyPiPairedResult(start, input(start, undefined, provider))).toBeUndefined()
  })

  it('refuses the end frame of another call or another tool, an update frame, and no frame', () => {
    expect(ohMyPiPairedResult(start, input({ type: 'tool_execution_end', toolCallId: 'd', toolName: 'read', result: {} }, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedResult(start, input({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'bash', result: {} }, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedResult(start, input({ type: 'tool_execution_update', toolCallId: 'c', toolName: 'read', partialResult: {} }, undefined, provider))).toBeUndefined()
    expect(ohMyPiPairedResult(start, undefined)).toBeUndefined()
  })

  it('refuses a pair when the row itself is no tool frame', () => {
    const end = input({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'read', result: {} }, undefined, provider)
    expect(ohMyPiPairedResult({ type: 'message_end' }, end)).toBeUndefined()
    expect(ohMyPiPairedResult(null, end)).toBeUndefined()
  })
})
