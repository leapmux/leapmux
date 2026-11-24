import type { FileAttachment } from '~/components/chat/attachments'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { AnsweringWorker, holdWorkerReplies, recordWorkerRequests, resetWorkerStandIns, settleWorkerReplies } from '~/test-support/fakeWorker'
import { createAgentInputEnqueueRetry, MAX_PENDING_ATTEMPTS } from './agentInputEnqueueRetry'

// This file runs in the node project, which runs no `vitest.setup.ts`. So it
// installs the stand-in Worker, which hashes each fingerprint in the real hash
// worker's handler, and it ends the holds and settles the replies after each
// test, as that setup does for a jsdom test.
beforeAll(() => {
  vi.stubGlobal('Worker', AnsweringWorker)
})

afterEach(async () => {
  resetWorkerStandIns()
  await settleWorkerReplies()
})

afterAll(() => {
  vi.unstubAllGlobals()
})

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
  it('reuses the input ID only while the complete payload is unchanged', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const first = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'hello',
      attachments: [
        attachment('a.txt', [1, 2, 3]),
      ],
      scheduledAt: '',
    }

    expect(await retry.attemptFor(first)).toMatchObject({ inputId: 'input-1' })
    expect(await retry.attemptFor({
      ...first,
      attachments: [
        attachment('a.txt', [1, 2, 3]),
      ],
    })).toMatchObject({ inputId: 'input-1' })
    expect(await retry.attemptFor({ ...first, text: 'changed' })).toMatchObject({ inputId: 'input-2' })
    expect(await retry.attemptFor({ ...first, agentId: 'agent-2' })).toMatchObject({ inputId: 'input-3' })
    expect(await retry.attemptFor({ ...first, kind: AgentInputKind.CONTROL_FEEDBACK })).toMatchObject({ inputId: 'input-4' })
    expect(await retry.attemptFor({
      ...first,
      attachments: [
        attachment('a.txt', [1, 2, 4]),
      ],
    })).toMatchObject({ inputId: 'input-5' })
  })

  it('mints a new input ID after the Worker accepts an attempt', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'hello',
      attachments: [] as FileAttachment[],
      scheduledAt: '',
    }
    const accepted = await retry.attemptFor(payload)

    retry.markAccepted(accepted)

    expect(await retry.attemptFor(payload)).toMatchObject({ inputId: 'input-2' })
  })

  it('keeps message and control-feedback attempts independent', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const message = {
      agentId: 'agent-1',
      kind: AgentInputKind.USER_MESSAGE,
      text: 'message',
      attachments: [] as FileAttachment[],
      scheduledAt: '',
    }

    expect(await retry.attemptFor(message)).toMatchObject({ inputId: 'input-1' })
    expect(await retry.attemptFor({ ...message, kind: AgentInputKind.CONTROL_FEEDBACK, text: 'feedback' })).toMatchObject({ inputId: 'input-2' })
    expect(await retry.attemptFor(message)).toMatchObject({ inputId: 'input-1' })
  })

  // A failed send leaves the text in the composer. The user can abandon it,
  // type a different message, and send that instead. Both attempts stay
  // pending, so a later re-send of either one recovers its own input ID rather
  // than minting a second ID for an input the Worker can already hold.
  it('keeps two abandoned attempts for one agent independent', async () => {
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

    const firstAttempt = await retry.attemptFor(first)
    const secondAttempt = await retry.attemptFor(second)

    expect(secondAttempt).not.toBe(firstAttempt)
    expect(await retry.attemptFor(first)).toBe(firstAttempt)
    expect(await retry.attemptFor(second)).toBe(secondAttempt)
  })

  it('drops the oldest attempt once the pending map is full', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const oldestId = (await retry.attemptFor(message('message-0'))).inputId
    const newestText = `message-${MAX_PENDING_ATTEMPTS}`
    let newestId = ''
    for (let index = 1; index <= MAX_PENDING_ATTEMPTS; index++)
      newestId = (await retry.attemptFor(message(`message-${index}`))).inputId

    // One attempt past the cap, so the oldest aged out and mints a fresh ID.
    expect((await retry.attemptFor(message('message-0'))).inputId).not.toBe(oldestId)
    // Every attempt inside the cap still recovers its own ID.
    expect((await retry.attemptFor(message(newestText))).inputId).toBe(newestId)
  })

  it('moves a repeated attempt to the newest slot so it does not age out', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const ids: string[] = []
    for (let index = 1; index <= MAX_PENDING_ATTEMPTS; index++)
      ids.push((await retry.attemptFor(message(`message-${index}`))).inputId)

    // Repeat the oldest attempt, then add one more past the cap.
    expect((await retry.attemptFor(message('message-1'))).inputId).toBe(ids[0])
    await retry.attemptFor(message('overflow'))

    expect((await retry.attemptFor(message('message-1'))).inputId).toBe(ids[0])
    expect((await retry.attemptFor(message('message-2'))).inputId).not.toBe(ids[1])
  })

  it('does not let a late acknowledgement clear a newer attempt', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = message('repeat this')
    const firstAttempt = await retry.attemptFor(payload)
    retry.observeAccepted(firstAttempt.agentId, firstAttempt.inputId)
    const secondAttempt = await retry.attemptFor(payload)
    expect(secondAttempt.inputId).not.toBe(firstAttempt.inputId)

    retry.markAccepted(firstAttempt)

    expect(await retry.attemptFor(payload)).toBe(secondAttempt)
  })
})

// The schedule is part of the payload identity: a resend that changes only the
// schedule must not reuse the input ID the Worker already bound to the old
// deadline.
describe('agent input enqueue retry schedule', () => {
  it('reuses the ID for an identical schedule and mints one for each change', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const scheduled = {
      ...message('later'),
      scheduledAt: '2030-01-01T09:00:00.000Z',
    }

    expect(await retry.attemptFor(scheduled)).toMatchObject({ inputId: 'input-1' })
    expect(await retry.attemptFor({ ...scheduled })).toMatchObject({ inputId: 'input-1' })
    expect(await retry.attemptFor({
      ...message('later'),
    })).toMatchObject({ inputId: 'input-2' })
    expect(await retry.attemptFor({
      ...message('later'),
      scheduledAt: '2031-01-01T09:00:00.000Z',
    })).toMatchObject({ inputId: 'input-3' })
  })
})

// The composer asks whether a send re-sends an unanswered attempt, so that an
// expired schedule does not force a changed payload and a second input ID.
describe('agent input enqueue retry pending attempts', () => {
  it('forgets exactly the attempts whose inputs a snapshot holds', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const shown = await retry.attemptFor(message('shown'))
    const hidden = await retry.attemptFor(message('hidden'))
    retry.markUncertain(shown)
    retry.markUncertain(hidden)

    retry.observeAccepted('another-agent', shown.inputId)
    retry.observeAccepted(shown.agentId, 'unrelated')
    expect(await retry.pendingAttemptFor(message('shown'))).toBe(shown)
    retry.observeAccepted(shown.agentId, shown.inputId)

    expect(await retry.pendingAttemptFor(message('shown'))).toBeUndefined()
    expect(await retry.pendingAttemptFor(message('hidden'))).toBe(hidden)
    expect(await retry.attemptFor(message('shown'))).toMatchObject({ inputId: 'input-3' })
  })

  it('acquires only an uncertain attempt until the Worker accepts it', async () => {
    const retry = createAgentInputEnqueueRetry(() => 'input-1')
    const scheduled = {
      ...message('later'),
      scheduledAt: '2030-01-01T09:00:00.000Z',
    }
    expect(await retry.pendingAttemptFor(scheduled)).toBeUndefined()
    const attempt = await retry.attemptFor(scheduled)
    expect(await retry.pendingAttemptFor(scheduled)).toBeUndefined()
    retry.markUncertain(attempt)
    expect(await retry.pendingAttemptFor({ ...scheduled })).toBe(attempt)
    retry.markAccepted(attempt)
    expect(await retry.pendingAttemptFor(scheduled)).toBeUndefined()
  })

  it('does not acquire a pending attempt for a payload that differs in any part', async () => {
    const retry = createAgentInputEnqueueRetry(() => 'input-1')
    const scheduled = {
      ...message('later'),
      scheduledAt: '2030-01-01T09:00:00.000Z',
    }
    retry.markUncertain(await retry.attemptFor(scheduled))
    for (const changed of [
      message('later'),
      { ...scheduled, text: 'other' },
      { ...scheduled, agentId: 'agent-2' },
      { ...scheduled, kind: AgentInputKind.CONTROL_FEEDBACK },
      {
        ...scheduled,
        attachments: [
          attachment('a.txt', [1]),
        ],
      },
    ])
      expect(await retry.pendingAttemptFor(changed)).toBeUndefined()
  })

  it('does not mint an id for an absent pending attempt', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    expect(await retry.pendingAttemptFor(message('hello'))).toBeUndefined()
    expect(await retry.attemptFor(message('hello'))).toMatchObject({ inputId: 'input-1' })
  })

  it('removes a known refusal but retains earlier delivery uncertainty', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = message('hello')
    const refused = await retry.attemptFor(payload)
    retry.markRefused(refused)
    const uncertain = await retry.attemptFor(payload)
    expect(uncertain).not.toBe(refused)
    retry.markUncertain(uncertain)
    retry.markRefused(uncertain)
    expect(await retry.pendingAttemptFor(payload)).toBe(uncertain)
  })

  it('detects changed attachment bytes when acquiring a pending attempt', async () => {
    const retry = createAgentInputEnqueueRetry()
    const payload = {
      ...message('hello'),
      attachments: [
        attachment('a.txt', [1, 2]),
      ],
    }
    retry.markUncertain(await retry.attemptFor(payload))
    payload.attachments[0]!.data[0] = 9
    expect(await retry.pendingAttemptFor(payload)).toBeUndefined()
  })
})

// The hash runs off the main thread, so a response can settle an attempt while a
// second call for the same payload still hashes. The call started while the
// attempt was pending, so it must join that attempt, as it did when the hash ran
// in one task: a fresh ID would deliver the payload twice.
describe('agent input enqueue retry during the hash', () => {
  /** Start a lookup whose hash waits in the hash worker until `release` runs. */
  function holdHash<T>(lookup: () => Promise<T>) {
    const hold = holdWorkerReplies('lib/fieldDigestWorker.ts')
    return {
      result: lookup(),
      release: async () => {
        // The hash of the lookup waits in the worker until now.
        await settleWorkerReplies()
        expect(hold.pending()).toBe(1)
        await hold.release()
      },
    }
  }

  it('returns the ID of an attempt that the Worker accepts during the hash', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const inFlight = await retry.attemptFor(message('deploy it'))

    const second = holdHash(() => retry.attemptFor(message('deploy it')))
    retry.markAccepted(inFlight)
    await second.release()

    expect(await second.result).toBe(inFlight)
    // The accepted attempt stays settled: a later send of the same text is a new message.
    expect(await retry.attemptFor(message('deploy it'))).toMatchObject({ inputId: 'input-2' })
  })

  it('returns the ID of an attempt that the Worker refuses during the hash', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const inFlight = await retry.attemptFor(message('deploy it'))

    const second = holdHash(() => retry.attemptFor(message('deploy it')))
    retry.markRefused(inFlight)
    await second.release()

    expect(await second.result).toBe(inFlight)
    expect(await retry.attemptFor(message('deploy it'))).toMatchObject({ inputId: 'input-2' })
  })

  it('acquires an uncertain attempt that the Worker accepts during the hash', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const uncertain = await retry.attemptFor(message('deploy it'))
    retry.markUncertain(uncertain)

    const retried = holdHash(() => retry.pendingAttemptFor(message('deploy it')))
    retry.markAccepted(uncertain)
    await retried.release()

    expect(await retried.result).toBe(uncertain)
    expect(await retry.pendingAttemptFor(message('deploy it'))).toBeUndefined()
  })

  it('mints one ID for two identical calls whose hashes run at the same time', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)

    const attempts = await Promise.all([
      retry.attemptFor(message('deploy it')),
      retry.attemptFor(message('deploy it')),
    ])

    expect(attempts[0]?.inputId).toBe('input-1')
    expect(attempts[1]).toBe(attempts[0])
  })
})

describe('agent input enqueue retry fingerprint', () => {
  const large = () => ({
    ...message('large'),
    attachments: [
      attachment('large.bin', Array.from({ length: 512 * 1024 + 1 }, (_, i) => i % 251)),
    ],
  })

  it('passes every field, attachment bytes included, to the hash worker', async () => {
    const requests = recordWorkerRequests('lib/fieldDigestWorker.ts')
    const retry = createAgentInputEnqueueRetry(() => 'input-1')
    const encoder = new TextEncoder()
    const payload = {
      ...message('hello'),
      scheduledAt: '2030-01-01T09:00:00.000Z',
      attachments: [
        attachment('a.txt', [1, 2, 3]),
        attachment('b.txt', [4]),
      ],
    }

    await retry.attemptFor(payload)

    expect(requests).toHaveLength(1)
    const { fields } = requests[0]!
    expect(fields.map(field => [...field])).toEqual([
      [
        ...encoder.encode('agent-1'),
      ],
      [AgentInputKind.USER_MESSAGE],
      [
        ...encoder.encode('hello'),
      ],
      [
        ...encoder.encode('2030-01-01T09:00:00.000Z'),
      ],
      [
        ...encoder.encode('a.txt'),
      ],
      [
        ...encoder.encode('text/plain'),
      ],
      [1, 2, 3],
      [
        ...encoder.encode('b.txt'),
      ],
      [
        ...encoder.encode('text/plain'),
      ],
      [4],
    ])
    // The attachment bytes go to the hash worker as they are: neither the
    // fingerprint nor the worker client copies a view that owns its buffer.
    expect(fields.at(-1)).toBe(payload.attachments[1]!.data)
  })

  it('detects a change in the last byte of a large attachment', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)
    const payload = large()
    const first = await retry.attemptFor(payload)
    const data = payload.attachments[0]!.data
    data[data.length - 1] = 255
    expect(await retry.attemptFor(payload)).not.toBe(first)
  })
})
