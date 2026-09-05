import type { FileAttachment } from '~/components/chat/attachments'
import { describe, expect, it } from 'vitest'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAgentInputEnqueueRetry } from './agentInputEnqueueRetry'

function attachment(filename: string, data: number[]): FileAttachment {
  const bytes = new Uint8Array(data)
  return {
    id: filename,
    file: new File([bytes], filename, { type: 'text/plain' }),
    filename,
    mimeType: 'text/plain',
    data: bytes,
    size: bytes.byteLength,
  }
}

describe('agent input enqueue retry', () => {
  it('reuses the input ID only while the complete payload is unchanged', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const first = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'hello',
      attachments: [attachment('a.txt', [1, 2, 3])],
    }

    expect(retry.inputIdFor(first)).toBe('input-1')
    expect(retry.inputIdFor({ ...first, attachments: [attachment('a.txt', [1, 2, 3])] })).toBe('input-1')
    expect(retry.inputIdFor({ ...first, text: 'changed' })).toBe('input-2')
    expect(retry.inputIdFor({ ...first, agentId: 'agent-2' })).toBe('input-3')
    expect(retry.inputIdFor({ ...first, kind: AgentInputKind.CONTROL_FEEDBACK })).toBe('input-4')
    expect(retry.inputIdFor({ ...first, attachments: [attachment('a.txt', [1, 2, 4])] })).toBe('input-5')
  })

  it('mints a new input ID after the Worker accepts an attempt', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'hello',
      attachments: [] as FileAttachment[],
    }
    const accepted = retry.inputIdFor(payload)

    retry.markAccepted(accepted)

    expect(retry.inputIdFor(payload)).toBe('input-2')
  })

  it('keeps message and control-feedback attempts independent', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const message = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'message',
      attachments: [] as FileAttachment[],
    }

    expect(retry.inputIdFor(message)).toBe('input-1')
    expect(retry.inputIdFor({ ...message, kind: AgentInputKind.CONTROL_FEEDBACK, text: 'feedback' })).toBe('input-2')
    expect(retry.inputIdFor(message)).toBe('input-1')
  })

  it('does not let a late acknowledgement clear a newer attempt', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const first = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'first',
      attachments: [] as FileAttachment[],
    }
    const firstId = retry.inputIdFor(first)
    const second = { ...first, text: 'second' }
    const secondId = retry.inputIdFor(second)

    retry.markAccepted(firstId)

    expect(retry.inputIdFor(second)).toBe(secondId)
  })
})
