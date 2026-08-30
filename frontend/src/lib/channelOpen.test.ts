import { describe, expect, it } from 'vitest'
import { MAX_CHUNK_SIZE, MAX_CONFIGURABLE_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { ChannelError } from './channelError'
import { ChannelOpen } from './channelOpen'
import { ChannelPool } from './channelPool'
import { ChannelRelay } from './channelRelay'
import { ChannelSession } from './channelSession'
import { KeyPinStore } from './keyPinStore'
import { maxReassembledMessageSize } from './reassembler'

function makeOpen(opts?: {
  testPayloadBudget?: number
  testReassembledCeiling?: number
}): ChannelOpen {
  return new ChannelOpen({
    transport: {
      getWorkerHandshakeParams: async () => {
        throw new Error('unused')
      },
      openChannel: async () => {
        throw new Error('unused')
      },
      closeChannel: async () => {},
    },
    keyPins: new KeyPinStore({ confirmKeyPin: async () => 'accept' }),
    session: new ChannelSession({
      sendToWire: () => {},
      closeChannel: async () => {},
      onSendFailure: () => {},
    }),
    relay: new ChannelRelay({
      createWebSocket: () => {
        throw new Error('unused')
      },
      wsOpenTimeoutMs: 1_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    }),
    pool: new ChannelPool(),
    expectedUserId: () => undefined,
    testPayloadBudget: opts?.testPayloadBudget,
    testReassembledCeiling: opts?.testReassembledCeiling,
    verifySession: async () => {},
    evictGhost: () => {},
    notifyStateChange: () => {},
  })
}

describe('channelOpen', () => {
  it('resolveMessageLimits adopts a negotiated in-bounds budget', () => {
    const open = makeOpen()
    const negotiated = MAX_CHUNK_SIZE + 100
    expect(open.resolveMessageLimits({ maxMessageSize: negotiated })).toEqual({
      payload: negotiated,
      reassembled: maxReassembledMessageSize(negotiated),
    })
  })

  it('resolveMessageLimits rejects out-of-bounds negotiated sizes', () => {
    const open = makeOpen()
    expect(() => open.resolveMessageLimits({ maxMessageSize: MAX_CHUNK_SIZE - 1 }))
      .toThrow(ChannelError)
    expect(() => open.resolveMessageLimits({ maxMessageSize: MAX_CONFIGURABLE_MESSAGE_SIZE + 1 }))
      .toThrow(/out of bounds/)
  })

  it('resolveMessageLimits fails closed when the hub omits max_message_size', () => {
    const open = makeOpen()
    expect(() => open.resolveMessageLimits({})).toThrow(/no max_message_size/)
    expect(() => open.resolveMessageLimits({ maxMessageSize: 0 })).toThrow(/no max_message_size/)
  })

  it('resolveMessageLimits uses test-only budgets when negotiation is omitted', () => {
    const open = makeOpen({ testPayloadBudget: 2048 })
    expect(open.resolveMessageLimits({})).toEqual({
      payload: 2048,
      reassembled: maxReassembledMessageSize(2048),
    })

    const both = makeOpen({ testPayloadBudget: 1024, testReassembledCeiling: 4096 })
    expect(both.resolveMessageLimits({})).toEqual({ payload: 1024, reassembled: 4096 })

    const ceilingOnly = makeOpen({ testReassembledCeiling: 512 })
    expect(ceilingOnly.resolveMessageLimits({})).toEqual({ payload: 512, reassembled: 512 })
  })

  it('resolveMessageLimits prefers negotiation over test budgets', () => {
    const open = makeOpen({ testPayloadBudget: 99 })
    const negotiated = MAX_CHUNK_SIZE
    expect(open.resolveMessageLimits({ maxMessageSize: negotiated }).payload).toBe(negotiated)
  })
})
