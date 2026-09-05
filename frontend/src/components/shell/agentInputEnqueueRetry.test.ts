import type { FileAttachment } from '~/components/chat/attachments'
import { describe, expect, it } from 'vitest'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAgentInputEnqueueRetry, MAX_PENDING_ATTEMPTS } from './agentInputEnqueueRetry'

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

function message(text: string) {
  return {
    agentId: 'agent-1',
    kind: AgentInputKind.USER_MESSAGE,
    text,
    attachments: [] as FileAttachment[],
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

  // A failed send leaves the text in the composer. The user can abandon it,
  // type a different message, and send that instead. Both attempts stay
  // pending, so a later re-send of either one recovers its own input ID rather
  // than minting a second ID for an input the Worker can already hold.
  it('keeps two abandoned attempts for one agent independent', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const first = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'first',
      attachments: [] as FileAttachment[],
    }
    const second = { ...first, text: 'second' }

    const firstId = retry.inputIdFor(first)
    const secondId = retry.inputIdFor(second)

    expect(secondId).not.toBe(firstId)
    expect(retry.inputIdFor(first)).toBe(firstId)
    expect(retry.inputIdFor(second)).toBe(secondId)
  })

  it('drops the oldest attempt once the pending map is full', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const oldestId = retry.inputIdFor(message('message-0'))
    const newestText = `message-${MAX_PENDING_ATTEMPTS}`
    let newestId = ''
    for (let index = 1; index <= MAX_PENDING_ATTEMPTS; index++)
      newestId = retry.inputIdFor(message(`message-${index}`))

    // One attempt past the cap, so the oldest aged out and mints a fresh ID.
    expect(retry.inputIdFor(message('message-0'))).not.toBe(oldestId)
    // Every attempt inside the cap still recovers its own ID.
    expect(retry.inputIdFor(message(newestText))).toBe(newestId)
  })

  it('moves a repeated attempt to the newest slot so it does not age out', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const ids: string[] = []
    for (let index = 1; index <= MAX_PENDING_ATTEMPTS; index++)
      ids.push(retry.inputIdFor(message(`message-${index}`)))

    // Repeat the oldest attempt, then add one more past the cap.
    expect(retry.inputIdFor(message('message-1'))).toBe(ids[0])
    retry.inputIdFor(message('overflow'))

    expect(retry.inputIdFor(message('message-1'))).toBe(ids[0])
    expect(retry.inputIdFor(message('message-2'))).not.toBe(ids[1])
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
