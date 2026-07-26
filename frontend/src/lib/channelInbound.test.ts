import type { InboundChannel } from './channelInbound'
import type { Session } from './noise'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  RekeyAckSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { ChannelInbound } from './channelInbound'
import { ChannelRpcMux } from './channelRpc'
import { ChannelSession } from './channelSession'
import { generateRekeyEphemeral } from './noise-hybrid'
import { Reassembler } from './reassembler'

function mockSession(): Session {
  // `satisfies Session` (not `as unknown as`) so a future CipherState method
  // addition fails to compile here instead of silently diverging the mock.
  // Each half carries the full CipherStateLike surface; the unused direction's
  // method is a no-op never called.
  const noop = () => new Uint8Array()
  return {
    send: {
      encrypt: (pt: Uint8Array) => new Uint8Array(pt),
      decrypt: noop,
      needsRekey: () => false,
      rekeyWithSecret: vi.fn(),
      clearPrev: vi.fn(),
      nonce: () => 0,
    },
    receive: {
      encrypt: noop,
      decrypt: (ct: Uint8Array) => new Uint8Array(ct),
      needsRekey: () => false,
      rekeyWithSecret: vi.fn(),
      clearPrev: vi.fn(),
      nonce: () => 0,
    },
  } satisfies Session
}

function makeChannel(overrides?: Partial<InboundChannel>): InboundChannel {
  return {
    channelId: 'ch-1',
    workerId: 'w1',
    session: mockSession(),
    maxReassembledMessageSize: 1024,
    pendingRequests: new Map(),
    streamListeners: new Map(),
    reassembly: new Reassembler(1024),
    nextRequestId: 1,
    state: 'verified',
    lastRekeyAt: 0,
    rekeyNotBefore: 0,
    rekeyWait: null,
    rekeyClear: null,
    rekeyAbort: null,
    rekeyRequestId: null,
    rekeyChain: Promise.resolve(),
    workerMlkemPub: new Uint8Array(0),
    rekeyMaterial: null,
    ...overrides,
  }
}

function makeInbound(ch: InboundChannel, opts?: {
  closeChannel?: (id: string) => void
  forgetClosedChannel?: (id: string) => void
}) {
  const session = new ChannelSession({
    sendToWire: () => {},
    closeChannel: async () => {},
    onSendFailure: () => {},
  })
  const rpc = new ChannelRpcMux({
    send: () => {},
    onSendFailure: () => {},
    rpcTimeoutFn: () => 50,
    notifyError: () => {},
    safeCall: (fn) => {
      try {
        fn()
      }
      catch { /* isolate */ }
    },
  })
  const forgetClosedChannel = opts?.forgetClosedChannel ?? vi.fn()
  const closeChannel = opts?.closeChannel ?? vi.fn()
  const inbound = new ChannelInbound({
    getChannel: id => id === ch.channelId ? ch : undefined,
    session,
    rpc,
    closeChannel,
    forgetClosedChannel,
  })
  return { inbound, session, rpc, forgetClosedChannel, closeChannel }
}

function encryptInner(ch: InboundChannel, plaintext: Uint8Array, correlationId: number, flags = ChannelMessageFlags.UNSPECIFIED) {
  const ciphertext = ch.session.receive.decrypt === ch.session.send.encrypt
    ? plaintext
    : plaintext
  // Decrypt path uses receive.decrypt which is identity in mockSession.
  void ciphertext
  return create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId: ch.channelId,
    ciphertext: plaintext,
    correlationId: BigInt(correlationId),
    flags,
  })
}

describe('channelInbound', () => {
  it('peer CLOSE flag drains and forgets the channel', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(1, { onMessage: () => {}, onEnd: () => {}, onError })
    const { inbound, forgetClosedChannel } = makeInbound(ch)
    inbound.handleMessage('ch-1', create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-1',
      ciphertext: new Uint8Array(),
      correlationId: 0n,
      flags: ChannelMessageFlags.CLOSE,
    }))
    expect(ch.state).toBe('closed')
    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]![0]).toBeInstanceOf(ChannelError)
    expect(forgetClosedChannel).toHaveBeenCalledWith('ch-1')
  })

  it('ignores frames for unknown channel ids', () => {
    const ch = makeChannel()
    const { inbound, forgetClosedChannel } = makeInbound(ch)
    inbound.handleMessage('missing', encryptInner(ch, new Uint8Array([1]), 1))
    expect(forgetClosedChannel).not.toHaveBeenCalled()
  })

  it('closes the channel when decrypt fails', () => {
    const ch = makeChannel({
      session: {
        send: { encrypt: () => new Uint8Array(), decrypt: () => new Uint8Array(), needsRekey: () => false, rekeyWithSecret: () => {}, clearPrev: () => {}, nonce: () => 0 },
        receive: {
          encrypt: () => new Uint8Array(),
          decrypt: () => {
            throw new Error('bad tag')
          },
          needsRekey: () => false,
          rekeyWithSecret: () => {},
          clearPrev: () => {},
          nonce: () => 0,
        },
      } satisfies Session,
    })
    const { inbound, closeChannel } = makeInbound(ch)
    inbound.handleMessage('ch-1', encryptInner(ch, new Uint8Array([1]), 1))
    expect(closeChannel).toHaveBeenCalledWith('ch-1')
  })

  it('delivers a unary response after decrypt', () => {
    const ch = makeChannel()
    const { inbound, rpc } = makeInbound(ch)
    const resolve = vi.fn()
    ch.pendingRequests.set(3, { resolve, reject: () => {} })
    const envelope = create(InnerMessageSchema, {
      kind: {
        case: 'response',
        value: create(InnerRpcResponseSchema, { isError: false, payload: new Uint8Array([7]) }),
      },
    })
    inbound.handleMessage('ch-1', encryptInner(ch, toBinary(InnerMessageSchema, envelope), 3))
    expect(resolve).toHaveBeenCalledOnce()
    expect(ch.pendingRequests.has(3)).toBe(false)
    void rpc
  })

  it('drops out-of-range correlation ids without closing', () => {
    const ch = makeChannel()
    const { inbound, closeChannel } = makeInbound(ch)
    inbound.handleMessage('ch-1', create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-1',
      ciphertext: new Uint8Array([1]),
      correlationId: BigInt(Number.MAX_SAFE_INTEGER) + 1n,
      flags: ChannelMessageFlags.UNSPECIFIED,
    }))
    expect(closeChannel).not.toHaveBeenCalled()
    expect(ch.state).toBe('verified')
  })

  it('routes rekeyAck through the session with correlation id', () => {
    const wait = vi.fn()
    // Use a real ephemeral so the fresh-DH derivation in handleRekeyOutcome
    // succeeds (the session itself is a mock; only the routing is under test).
    const eph = generateRekeyEphemeral()
    const ch = makeChannel({
      rekeyWait: wait,
      rekeyRequestId: 9,
      rekeyMaterial: { ePriv: eph.privateKey, mlkemSS: null },
    })
    const { inbound } = makeInbound(ch)
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'rekeyAck', value: create(RekeyAckSchema, { dhPub: eph.publicKey }) },
    })
    inbound.handleMessage('ch-1', encryptInner(ch, toBinary(InnerMessageSchema, envelope), 9))
    expect(wait).toHaveBeenCalledWith(true, 0)
  })
})
