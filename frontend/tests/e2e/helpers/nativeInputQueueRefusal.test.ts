import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { expectNativeInputRefusal } from './nativeInputQueueRefusal'

const { currentNativeAgent, readNativeInputQueue } = vi.hoisted(() => ({
  currentNativeAgent: vi.fn(),
  readNativeInputQueue: vi.fn(),
}))
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
})
