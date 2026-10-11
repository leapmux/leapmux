import type { Page } from '@playwright/test'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { InnerStreamMessageSchema } from '../../../src/generated/proto/leapmux/v1/channel_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { AGENT_WATCH_UPDATE_ID } from '../helpers/agentEventWatch'
import { waitForKiloPromptEnd } from './goalScenario'

const { stream, cancel, selectedAgentTabId } = vi.hoisted(() => ({ stream: vi.fn(), cancel: vi.fn(), selectedAgentTabId: vi.fn() }))

vi.mock('../helpers/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/api')>()
  return { ...actual, getTestChannel: async () => ({ getOrOpenChannel: async () => 'kilo-native-channel', stream }) }
})

vi.mock('../helpers/nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/nativeScenario')>()
  return { ...actual, selectedAgentTabId }
})

describe('waitForKiloPromptEnd', () => {
  it('rejects an absent authoritative queue snapshot instead of reporting completion', async () => {
    const agentId = 'kilo-native-agent'
    selectedAgentTabId.mockResolvedValue(agentId)
    stream.mockReturnValue({
      onMessage: (listener: (message: { payload: Uint8Array }) => void) => queueMicrotask(() => {
        listener(create(InnerStreamMessageSchema, {
          payload: toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: {
            updateId: AGENT_WATCH_UPDATE_ID,
            agentStates: [{ agentId, mode: WatchMode.FULL, replayId: AGENT_WATCH_UPDATE_ID }],
          } } })),
        }))
        listener(create(InnerStreamMessageSchema, {
          payload: toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: { agentId, event: { case: 'inputQueueChanged', value: {} } } } })),
        }))
      }),
      onError: () => {},
      onEnd: () => {},
      cancel,
    })
    const page = {} as unknown as Page
    const leapmuxServer = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'mock-worker' }
    const modelScript = { testDeadline: () => Date.now() + 240_000 }
    const outcome = await waitForKiloPromptEnd({ page, leapmuxServer, modelScript }).then(
      () => undefined,
      (error: unknown) => error,
    )

    expect(outcome).toBeInstanceOf(Error)
    expect(outcome).toMatchObject({ message: expect.stringContaining('queue snapshot') })
    expect(selectedAgentTabId).toHaveBeenCalledExactlyOnceWith(page)
    expect(stream).toHaveBeenCalledOnce()
    const request = stream.mock.calls[0]
    expect(request).toBeDefined()
    if (!request)
      throw new Error('The Kilo completion wait did not create its native stream.')
    expect(request[0]).toBe('kilo-native-channel')
    expect(request[1]).toBe('WatchEvents')
    expect(fromBinary(WatchEventsRequestSchema, request[2])).toMatchObject({ agents: [{ agentId }] })
    expect(cancel).toHaveBeenCalledExactlyOnceWith()
  })

  it('reads no queue when the selected tab has no agent ID', async () => {
    const failure = new Error('The selected agent tab has no agent ID in its data-tab-id attribute.')
    selectedAgentTabId.mockRejectedValue(failure)
    stream.mockClear()
    const page = {} as unknown as Page
    const leapmuxServer = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'mock-worker' }
    const modelScript = { testDeadline: () => Date.now() + 240_000 }

    await expect(waitForKiloPromptEnd({ page, leapmuxServer, modelScript })).rejects.toBe(failure)
    expect(stream).not.toHaveBeenCalled()
  })
})
