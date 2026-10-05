import type { ToolSpanRole } from '~/lib/messageSpan'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { prepareChatRow } from '../../../rowPreparation'
import { input } from '../../testUtils'
import { classifyDeepseekHarnessMessage } from '../classification'
import { deepseekHarnessExtractRow } from './row'
import '../plugin'

const provider = AgentProvider.DEEPSEEK_HARNESS
const request = {
  type: 'tool/call',
  seq: 1,
  time: 1000,
  data: { callId: 'native-role-call', name: 'bash', arguments: '{"command":"printf native"}' },
}
const result = {
  type: 'tool/result',
  seq: 2,
  time: 1001,
  data: { message: { toolCallId: 'native-role-call', content: [{ type: 'text', text: 'Native bytes.' }] } },
}

function extract(frame: Record<string, unknown>, role: ToolSpanRole) {
  const resolved = input(frame, null, provider)
  return deepseekHarnessExtractRow({
    resolved,
    category: classifyDeepseekHarnessMessage(resolved),
    span: { request: input(request, null, provider), result: undefined, role, visibleRows: { request: false, result: false } },
  })
}

describe('deepseekHarnessExtractRow', () => {
  it.each(['request', 'result'] as const)('refuses an explicit no-side context for a native %s', (side) => {
    const frame = side === 'request' ? request : result
    expect(extract(frame, 'none')).toBeNull()
    expect(extract(frame, side)).toMatchObject({ kind: 'tool', role: side, call: { id: 'native-role-call' } })
  })

  it.each(['request', 'result'] as const)('keeps the unknown-role category fallback for a native %s', (side) => {
    expect(extract(side === 'request' ? request : result, 'other')).toMatchObject({ kind: 'tool', role: side })
  })
})

// Source: a live native child (`@deepseek-ai/dsh` 0.2.0-rc.2) opens with one `user/message` per input.
// The first one holds the prompt as two text blocks, and the native process adds the second block.
describe('native user message of a child session', () => {
  const nativePrompt = (content: unknown[]) => ({
    type: 'user/message',
    seq: 8,
    time: 1000,
    data: { content, source: { kind: 'user' }, role: 'user', id: 'native-message' },
    surfaceOp: 'append',
  })

  function rowOf(frame: unknown) {
    const { extraction } = prepareChatRow(makeMessage({ agentProvider: provider, source: MessageSource.USER, content: rawContent(frame) }))
    return extraction
  }

  it('draws the native prompt as a user row with its text blocks in their native order', () => {
    const frame = nativePrompt([{ type: 'text', text: 'DEEPSEEKCHILD read the file. ' }, { type: 'text', text: 'Your parent agent id is "native-root".' }])
    expect(rowOf(frame)).toMatchObject({ kind: 'row', row: { kind: 'user', text: 'DEEPSEEKCHILD read the file. Your parent agent id is "native-root".', attachments: [] } })
  })

  it('names the attachments of the native prompt', () => {
    const frame = nativePrompt([{ type: 'text', text: 'Look at this.' }, { type: 'image', attachment: { name: 'shot.png', mediaType: 'image/png' } }])
    expect(rowOf(frame)).toMatchObject({ kind: 'row', row: { kind: 'user', text: 'Look at this.', attachments: [{ filename: 'shot.png', mimeType: 'image/png' }] } })
  })

  it('hides a native user message that holds no text and no attachment', () => {
    expect(rowOf(nativePrompt([{ type: 'text', text: '' }]))).toMatchObject({ kind: 'row', row: { kind: 'hidden' } })
  })

  it('still draws the user row that LeapMux stores without a native envelope', () => {
    expect(rowOf({ content: 'typed by the reader' })).toMatchObject({ kind: 'row', row: { kind: 'user', text: 'typed by the reader', attachments: [] } })
  })
})
