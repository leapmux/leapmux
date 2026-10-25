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
    scheduledAt: '',
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
      scheduledAt: '',
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
      scheduledAt: '',
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
      scheduledAt: '',
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
      scheduledAt: '',
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
      scheduledAt: '',
    }
    const firstId = retry.inputIdFor(first)
    const second = { ...first, text: 'second' }
    const secondId = retry.inputIdFor(second)

    retry.markAccepted(firstId)

    expect(retry.inputIdFor(second)).toBe(secondId)
  })
})

// The schedule is part of the payload identity: a resend that changes only the
// schedule must not reuse the input ID the Worker already bound to the old
// deadline.
describe('agent input enqueue retry schedule', () => {
  it('reuses the ID for an identical schedule and mints one for each change', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const scheduled = { ...message('later'), scheduledAt: '2030-01-01T09:00:00.000Z' }

    expect(retry.inputIdFor(scheduled)).toBe('input-1')
    expect(retry.inputIdFor({ ...scheduled })).toBe('input-1')
    expect(retry.inputIdFor({ ...message('later') })).toBe('input-2')
    expect(retry.inputIdFor({ ...message('later'), scheduledAt: '2031-01-01T09:00:00.000Z' })).toBe('input-3')
  })
})

// The composer asks whether a send re-sends an unanswered attempt, so that an
// expired schedule does not force a changed payload and a second input ID.
describe('agent input enqueue retry pending attempts', () => {
  it('acquires only an uncertain attempt until the Worker accepts it', () => {
    const retry = createAgentInputEnqueueRetry(() => 'input-1')
    const scheduled = { ...message('later'), scheduledAt: '2030-01-01T09:00:00.000Z' }
    expect(retry.inputIdForPendingAttempt(scheduled)).toBeUndefined()
    const id = retry.inputIdFor(scheduled)
    expect(retry.inputIdForPendingAttempt(scheduled)).toBeUndefined()
    retry.markUncertain(id)
    expect(retry.inputIdForPendingAttempt({ ...scheduled })).toBe(id)
    retry.markAccepted(id)
    expect(retry.inputIdForPendingAttempt(scheduled)).toBeUndefined()
  })

  it('does not acquire a pending attempt for a payload that differs in any part', () => {
    const retry = createAgentInputEnqueueRetry(() => 'input-1')
    const scheduled = { ...message('later'), scheduledAt: '2030-01-01T09:00:00.000Z' }
    retry.markUncertain(retry.inputIdFor(scheduled))
    for (const changed of [
      message('later'),
      { ...scheduled, text: 'other' },
      { ...scheduled, agentId: 'agent-2' },
      { ...scheduled, kind: AgentInputKind.CONTROL_FEEDBACK },
      { ...scheduled, attachments: [attachment('a.txt', [1])] },
    ])
      expect(retry.inputIdForPendingAttempt(changed)).toBeUndefined()
  })

  it('does not mint an id for an absent pending attempt', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    expect(retry.inputIdForPendingAttempt(message('hello'))).toBeUndefined()
    expect(retry.inputIdFor(message('hello'))).toBe('input-1')
  })

  it('removes a known refusal but retains earlier delivery uncertainty', () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = message('hello')
    const refused = retry.inputIdFor(payload)
    retry.markRefused(refused)
    const uncertain = retry.inputIdFor(payload)
    expect(uncertain).not.toBe(refused)
    retry.markUncertain(uncertain)
    retry.markRefused(uncertain)
    expect(retry.inputIdForPendingAttempt(payload)).toBe(uncertain)
  })

  it('detects changed attachment bytes when acquiring a pending attempt', () => {
    const retry = createAgentInputEnqueueRetry()
    const payload = { ...message('hello'), attachments: [attachment('a.txt', [1, 2])] }
    retry.markUncertain(retry.inputIdFor(payload))
    payload.attachments[0]!.data[0] = 9
    expect(retry.inputIdForPendingAttempt(payload)).toBeUndefined()
  })
})
