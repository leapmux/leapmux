import type { OpeningChannel } from './channelOpen'
import type { SessionHandshake } from './channelSessionClient'
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_MAX_MESSAGE_SIZE, MAX_CHUNK_SIZE, MAX_CONFIGURABLE_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { ChannelOpen } from './channelOpen'
import { ChannelPool } from './channelPool'
import { KeyPinStore } from './keyPinStore'
import { maxReassembledMessageSize } from './reassembler'

const keys = {
  x25519PublicKey: new Uint8Array(32),
  mlkemPublicKey: new Uint8Array(1568),
  slhdsaPublicKey: new Uint8Array(64),
}

interface OpenResult {
  channelId: string
  handshakePayload: Uint8Array
  userId: string
  maxMessageSize?: number
}

/**
 * A ChannelOpen over fakes that record each step in `steps`, in call order. Each
 * fake succeeds unless `overrides` replaces it.
 */
function openHarness(overrides?: {
  openChannel?: () => Promise<OpenResult>
  finishHandshake?: () => Promise<void>
  ensureWebSocket?: () => Promise<void>
  testPayloadBudget?: number
  testReassembledCeiling?: number
}) {
  const steps: string[] = []
  const handshake: SessionHandshake = {
    id: 7,
    message1: new Uint8Array([1, 6, 1, 6]),
  }
  const session = {
    prepare: vi.fn(() => {
      steps.push('prepare')
    }),
    beginHandshake: vi.fn(async () => {
      steps.push('beginHandshake')
      return handshake
    }),
    finishHandshake: vi.fn(async () => {
      steps.push('finishHandshake')
      await overrides?.finishHandshake?.()
    }),
    abandonHandshake: vi.fn(() => {
      steps.push('abandonHandshake')
    }),
    close: vi.fn((channelId: string) => {
      steps.push(`close ${channelId}`)
    }),
  }
  const transport = {
    getWorkerHandshakeParams: vi.fn(async () => {
      steps.push('getWorkerHandshakeParams')
      return { keys, encryptionMode: EncryptionMode.POST_QUANTUM }
    }),
    openChannel: vi.fn(async (): Promise<OpenResult> => {
      steps.push('openChannel')
      return overrides?.openChannel?.() ?? {
        channelId: 'ch-1',
        handshakePayload: new Uint8Array([2]),
        userId: 'user-1',
        maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
      }
    }),
    closeChannel: vi.fn(async (channelId: string) => {
      steps.push(`hub close ${channelId}`)
    }),
  }
  const relay = {
    ensureWebSocket: vi.fn(async () => {
      steps.push('ensureWebSocket')
      await overrides?.ensureWebSocket?.()
    }),
  }
  const pool = new ChannelPool<OpeningChannel>()
  const evictGhost = vi.fn((channelId: string) => {
    pool.delete(channelId)
  })
  const open = new ChannelOpen<OpeningChannel>({
    transport,
    keyPins: new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    }),
    session,
    relay,
    pool,
    expectedUserId: () => undefined,
    // Optional knobs: omit rather than pass an explicit undefined.
    ...(overrides?.testPayloadBudget === undefined
      ? {}
      : { testPayloadBudget: overrides.testPayloadBudget }),
    ...(overrides?.testReassembledCeiling === undefined
      ? {}
      : { testReassembledCeiling: overrides.testReassembledCeiling }),
    verifySession: async (channel) => {
      steps.push('verifySession')
      channel.state = 'verified'
    },
    evictGhost,
    notifyStateChange: () => {},
  })
  return {
    open,
    steps,
    handshake,
    session,
    transport,
    relay,
    pool,
    evictGhost,
  }
}

/** A ChannelOpen for the resolveMessageLimits tests, which never opens a channel. */
function makeOpen(opts?: {
  testPayloadBudget?: number
  testReassembledCeiling?: number
}): ChannelOpen {
  return openHarness(opts).open
}

describe('channelOpen', () => {
  it('adopts a negotiated budget within the allowed range in resolveMessageLimits', () => {
    const open = makeOpen()
    const negotiated = MAX_CHUNK_SIZE + 100
    expect(open.resolveMessageLimits({ maxMessageSize: negotiated })).toEqual({
      payload: negotiated,
      reassembled: maxReassembledMessageSize(negotiated),
    })
  })

  it('rejects negotiated sizes outside the allowed range in resolveMessageLimits', () => {
    const open = makeOpen()
    expect(() => open.resolveMessageLimits({ maxMessageSize: MAX_CHUNK_SIZE - 1 }))
      .toThrow(ChannelError)
    expect(() => open.resolveMessageLimits({ maxMessageSize: MAX_CONFIGURABLE_MESSAGE_SIZE + 1 }))
      .toThrow(/out of bounds/)
  })

  it('fails closed in resolveMessageLimits when the hub omits max_message_size', () => {
    const open = makeOpen()
    expect(() => open.resolveMessageLimits({})).toThrow(/no max_message_size/)
    expect(() => open.resolveMessageLimits({ maxMessageSize: 0 })).toThrow(/no max_message_size/)
  })

  it('uses test-only budgets in resolveMessageLimits when negotiation is omitted', () => {
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

  it('prefers negotiation over test budgets in resolveMessageLimits', () => {
    const open = makeOpen({ testPayloadBudget: 99 })
    const negotiated = MAX_CHUNK_SIZE
    expect(open.resolveMessageLimits({ maxMessageSize: negotiated }).payload).toBe(negotiated)
  })
})

describe('ChannelOpen openUncached', () => {
  it('prepares the session worker before the first Hub request', async () => {
    const h = openHarness()

    await h.open.openUncached('w1')

    expect(h.steps.indexOf('prepare')).toBe(0)
    expect(h.steps.indexOf('prepare')).toBeLessThan(h.steps.indexOf('getWorkerHandshakeParams'))
  })

  it('hands the handshake to finishHandshake, and does not abandon it on success', async () => {
    const h = openHarness()

    await expect(h.open.openUncached('w1')).resolves.toBe('ch-1')

    expect(h.transport.openChannel).toHaveBeenCalledWith('w1', h.handshake.message1)
    expect(h.session.finishHandshake).toHaveBeenCalledExactlyOnceWith(h.handshake, {
      channelId: 'ch-1',
      payload: new Uint8Array([2]),
      maxReassembledMessageSize: maxReassembledMessageSize(DEFAULT_MAX_MESSAGE_SIZE),
    })
    expect(h.session.abandonHandshake).not.toHaveBeenCalled()
    expect(h.session.close).not.toHaveBeenCalled()
    expect(h.transport.closeChannel).not.toHaveBeenCalled()
    expect(h.pool.get('ch-1')).toMatchObject({ state: 'verified', userId: 'user-1', maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE })
    // The handshake finishes before the socket, and the session is proven last.
    expect(h.steps.slice(h.steps.indexOf('finishHandshake'))).toEqual(['finishHandshake', 'ensureWebSocket', 'verifySession'])
  })

  it('opens nothing at the Hub, and abandons nothing, when the session worker cannot begin the handshake', async () => {
    const h = openHarness()
    h.session.beginHandshake.mockRejectedValueOnce(new ChannelError('transport', 'channel session worker could not start: blocked by CSP'))

    await expect(h.open.openUncached('w1')).rejects.toThrow('channel session worker could not start: blocked by CSP')

    expect(h.transport.openChannel).not.toHaveBeenCalled()
    expect(h.transport.closeChannel).not.toHaveBeenCalled()
    expect(h.session.abandonHandshake).not.toHaveBeenCalled()
    expect(h.session.close).not.toHaveBeenCalled()
    expect(h.pool.size).toBe(0)
  })

  it('abandons the handshake when the Hub refuses the open', async () => {
    const h = openHarness({
      openChannel: async () => {
        throw new ChannelError('rpc', 'worker offline')
      },
    })

    await expect(h.open.openUncached('w1')).rejects.toThrow('worker offline')

    expect(h.session.abandonHandshake).toHaveBeenCalledExactlyOnceWith(h.handshake)
    expect(h.session.finishHandshake).not.toHaveBeenCalled()
    expect(h.transport.closeChannel).not.toHaveBeenCalled()
  })

  it('abandons the handshake and rolls the Hub back when max_message_size is missing', async () => {
    const h = openHarness({
      openChannel: async () => ({
        channelId: 'ch-1',
        handshakePayload: new Uint8Array([2]),
        userId: 'user-1',
      }),
    })

    await expect(h.open.openUncached('w1')).rejects.toThrow(/no max_message_size/)

    expect(h.session.abandonHandshake).toHaveBeenCalledExactlyOnceWith(h.handshake)
    expect(h.session.finishHandshake).not.toHaveBeenCalled()
    expect(h.transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
  })

  it('abandons the handshake and rolls the Hub back when the Hub names no user', async () => {
    const h = openHarness({
      openChannel: async () => ({
        channelId: 'ch-1',
        handshakePayload: new Uint8Array([2]),
        userId: '',
        maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
      }),
    })

    await expect(h.open.openUncached('w1')).rejects.toThrow(/empty authenticated user id/)

    expect(h.session.abandonHandshake).toHaveBeenCalledExactlyOnceWith(h.handshake)
    expect(h.transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
  })

  it('rolls the Hub back, and abandons nothing, when the handshake fails to finish', async () => {
    const h = openHarness({
      finishHandshake: async () => {
        throw new ChannelError('transport', 'noise-hybrid: SLH-DSA signature verification failed')
      },
    })

    await expect(h.open.openUncached('w1')).rejects.toThrow(/SLH-DSA signature verification failed/)

    // The finish spent the handshake: the session worker zeroed it.
    expect(h.session.abandonHandshake).not.toHaveBeenCalled()
    expect(h.transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
    expect(h.pool.get('ch-1')).toBeUndefined()
  })

  it('closes the worker session and rolls the Hub back when the WebSocket fails after the handshake finished', async () => {
    const h = openHarness({
      ensureWebSocket: async () => {
        throw new ChannelError('transport', 'WebSocket open timed out')
      },
    })

    await expect(h.open.openUncached('w1')).rejects.toThrow('WebSocket open timed out')

    expect(h.session.close).toHaveBeenCalledExactlyOnceWith('ch-1')
    expect(h.transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
    expect(h.session.abandonHandshake).not.toHaveBeenCalled()
    expect(h.evictGhost).toHaveBeenCalledOnce()
  })
})
