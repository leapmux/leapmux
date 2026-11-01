import type { Page } from '@playwright/test'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputQueuePauseReason, AgentInputQueueSnapshotSchema, AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNativeInputRefusal } from './nativeInputQueueRefusal'

const { currentNativeAgent, readNativeInputQueue, rows } = vi.hoisted(() => ({
  currentNativeAgent: vi.fn(),
  readNativeInputQueue: vi.fn(),
  /** Each queue row that the helper read, with the text that it required. */
  rows: [] as Array<{ testId: string, text: string }>,
}))

// One attempt, so a failed check fails the test at once. A unit test has no test deadline, so the real wait would
// retry the failure with no limit.
vi.mock('./retryUntilPass', () => ({
  retryUntilPass: async <T>(attempt: () => T | Promise<T>) => attempt(),
}))

vi.mock('./ui', () => ({
  inputQueue: () => ({
    getByTestId: (testId: string) => ({ queueRow: testId }),
  }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown, message?: string) => {
      if (typeof value === 'object' && value !== null && 'queueRow' in value && typeof value.queueRow === 'string') {
        const testId = value.queueRow
        return {
          toContainText: async (text: string) => {
            rows.push({ testId, text })
          },
        }
      }
      return expect(value, message)
    },
  }
})
vi.mock('./nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeScenario')>()
  return { ...actual, currentNativeAgent }
})
vi.mock('./nativeInputQueueIdle', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeInputQueueIdle')>()
  return { ...actual, readNativeInputQueue }
})

afterEach(() => {
  currentNativeAgent.mockReset()
  readNativeInputQueue.mockReset()
  rows.length = 0
})

const native = {
  page: {} as Page,
  leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' },
}

describe('expectNativeInputRefusal', () => {
  it.each(['', '   '])('refuses the blank reason %j before it reads the agent or its queue', async (reason) => {
    await expect(expectNativeInputRefusal(native, reason)).rejects.toThrow('A refusal check needs the reason that the Worker states.')
    expect(currentNativeAgent).not.toHaveBeenCalled()
    expect(readNativeInputQueue).not.toHaveBeenCalled()
  })

  /** A queue that holds one input that the Worker refused, with no running turn. */
  function refusedQueue(pauseReason: AgentInputQueuePauseReason) {
    return create(AgentInputQueueSnapshotSchema, {
      revision: 4n,
      activeTurn: false,
      pauseReason,
      items: [
        {
          id: 'input-1',
          state: AgentInputState.FAILED,
          error: 'invalid queued agent input: the provider refused the image',
        },
      ],
    })
  }

  it('returns the refused item of a paused queue, and reads the reason in its row', async () => {
    currentNativeAgent.mockResolvedValue({ id: 'agent-1' })
    readNativeInputQueue.mockResolvedValue(refusedQueue(AgentInputQueuePauseReason.DELIVERY_FAILED))
    await expect(expectNativeInputRefusal(native, 'refused the image')).resolves.toMatchObject({
      id: 'input-1',
      state: AgentInputState.FAILED,
    })
    expect(readNativeInputQueue).toHaveBeenCalledExactlyOnceWith(native.leapmuxServer, 'agent-1')
    expect(rows).toEqual([
      { testId: 'queued-input-input-1', text: 'refused the image' },
    ])
  })

  it('fails while the Worker reports the queue unpaused, and reads no row', async () => {
    currentNativeAgent.mockResolvedValue({ id: 'agent-1' })
    readNativeInputQueue.mockResolvedValue(refusedQueue(AgentInputQueuePauseReason.UNSPECIFIED))
    await expect(expectNativeInputRefusal(native, 'refused the image')).rejects.toThrow('expected +0 not to be +0')
    expect(rows).toEqual([])
  })
})
