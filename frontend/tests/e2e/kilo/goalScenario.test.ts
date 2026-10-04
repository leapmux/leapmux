import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { InnerStreamMessageSchema } from '../../../src/generated/proto/leapmux/v1/channel_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { waitForKiloPromptEnd } from './goalScenario'

const { stream, cancel } = vi.hoisted(() => ({ stream: vi.fn(), cancel: vi.fn() }))

vi.mock('../helpers/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/api')>()
  return { ...actual, getTestChannel: async () => ({ getOrOpenChannel: async () => 'kilo-native-channel', stream }) }
})

describe('waitForKiloPromptEnd', () => {
  it('rejects an absent authoritative queue snapshot instead of reporting completion', async () => {
    const agentId = 'kilo-native-agent'
    stream.mockReturnValue({
      onMessage: (listener: (message: { payload: Uint8Array }) => void) => queueMicrotask(() => {
        listener(create(InnerStreamMessageSchema, {
          payload: toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: { updateId: 1n } } })),
        }))
        listener(create(InnerStreamMessageSchema, {
          payload: toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: { agentId, event: { case: 'inputQueueChanged', value: {} } } } })),
        }))
      }),
      onError: () => {},
      onEnd: () => {},
      cancel,
    })
    const page = {
      locator: () => ({ first: () => ({ getAttribute: async () => agentId }) }),
    }
    const server = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'mock-worker' }
    const modelScript = { testDeadline: () => Date.now() + 240_000 }
    const outcome = await Reflect.apply(waitForKiloPromptEnd, undefined, [page, server, modelScript]).then(
      () => undefined,
      (error: unknown) => error,
    )

    expect(outcome).toBeInstanceOf(Error)
    expect(outcome).toMatchObject({ message: expect.stringContaining('queue snapshot') })
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
})
