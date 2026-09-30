import { describe, expect, it } from 'vitest'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyQoderMessage } from './classification'

describe('classifyQoderMessage', () => {
  it('keeps a completed native compaction boundary as a notification', () => {
    const frame = {
      type: QODER_FRAME_KIND.System,
      subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary,
      compact_metadata: { trigger: 'manual', pre_tokens: 8000, post_tokens: 400 },
    }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual', pre: 8000, post: 400 } }],
    })
  })

  it('keeps a compacting status inside a notification thread', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' }
    expect(classifyQoderMessage(input(undefined, { old_seqs: [], messages: [frame] }, AgentProvider.QODER))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'start' }],
    })
  })

  it('hides a final compacting status inside a notification thread', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: null }
    expect(classifyQoderMessage(input(undefined, { old_seqs: [], messages: [frame] }, AgentProvider.QODER))).toEqual({ kind: 'hidden' })
  })

  it.each([
    { type: QODER_FRAME_KIND.System, subtype: 'init' },
    { type: QODER_FRAME_KIND.StreamEvent, event: { type: 'message_start' } },
    { type: QODER_FRAME_KIND.CommandLifecycle, command: 'compact' },
    { type: QODER_FRAME_KIND.ToolProgress, tool_use_id: 'tool-1' },
    { type: QODER_FRAME_KIND.Progress, status: 'running' },
    { type: QODER_FRAME_KIND.Attachment, name: 'file.txt' },
  ])('hides a native bookkeeping frame %j', (frame) => {
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'hidden' })
  })

  it('keeps an unknown future frame visible for diagnosis', () => {
    expect(classifyQoderMessage(input({ type: 'future_frame' }, null, AgentProvider.QODER))).toEqual({ kind: 'unknown' })
  })

  it('hides the native echo of a user prompt', () => {
    const frame = { type: 'user', message: { role: 'user', content: [{ type: 'text', text: 'Read the image.' }] } }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'hidden' })
  })

  it('hides an empty provisional tool result before its image arrives', () => {
    const frame = { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content: [] }] } }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'hidden' })
  })

  it.each([
    '',
    [{ type: 'text', text: '' }],
  ])('hides an empty result body %j', (content) => {
    const frame = { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content }] } }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'hidden' })
  })

  it('shows a malformed result as raw content', () => {
    const frame = { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content: { unexpected: true } }] } }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'unknown' })
  })

  it('keeps a tool result with native image bytes', () => {
    const frame = {
      type: 'user',
      message: {
        role: 'user',
        content: [{
          type: 'tool_result',
          tool_use_id: 'read-1',
          content: [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'iVBORw0KGgo=' } }],
        }],
      },
    }
    expect(classifyQoderMessage(input(frame, null, AgentProvider.QODER))).toEqual({ kind: 'tool_result' })
  })
})
