import type { InboundChannel } from './channelInbound'
import type { Session } from './noise'
import type { LoggerCapture } from '~/test-support/loggerCapture'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  RekeyAckSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { makeIdentitySession } from '~/test-support/identitySession'
import { captureLoggerOutput } from '~/test-support/loggerCapture'
import { ChannelError } from './channelError'
import { ChannelInbound } from './channelInbound'
import { ChannelRpcMux } from './channelRpc'
import { ChannelSession } from './channelSession'
import { generateRekeyEphemeral } from './noise-hybrid'
import { Reassembler } from './reassembler'

function makeChannel(overrides?: Partial<InboundChannel>): InboundChannel {
  return {
    channelId: 'ch-1',
    workerId: 'w1',
    session: makeIdentitySession(),
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
    getChannel: id => id === ch.channelId
      ? ch
      : undefined,
    session,
    rpc,
    closeChannel,
    forgetClosedChannel,
  })
  return {
    inbound,
    session,
    rpc,
    forgetClosedChannel,
    closeChannel,
  }
}

function encryptInner(ch: InboundChannel, plaintext: Uint8Array, correlationId: number, flags = ChannelMessageFlags.UNSPECIFIED) {
  // Identity-session fixtures use the plaintext as their ciphertext.
  return create(ChannelMessageSchema, {
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
    ch.streamListeners.set(1, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { inbound, forgetClosedChannel } = makeInbound(ch)
    inbound.handleMessage('ch-1', create(ChannelMessageSchema, {
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
        send: {
          encrypt: () => new Uint8Array(),
          decrypt: () => new Uint8Array(),
          needsRekey: () => false,
          rekeyWithSecret: () => {},
          clearPrev: () => {},
          nonce: () => 0,
        },
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
    ch.pendingRequests.set(3, {
      resolve,
      reject: () => {},
    })
    const envelope = create(InnerMessageSchema, {
      kind: {
        case: 'response',
        value: create(InnerRpcResponseSchema, {
          payload: new Uint8Array([7]),
        }),
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
      kind: {
        case: 'rekeyAck',
        value: create(RekeyAckSchema, { dhPub: eph.publicKey }),
      },
    })
    inbound.handleMessage('ch-1', encryptInner(ch, toBinary(InnerMessageSchema, envelope), 9))
    expect(wait).toHaveBeenCalledWith(true, 0)
  })
})

const RETIRED_ID_MESSAGE = 'dropped a late frame for a retired correlation id'
const NEVER_ALLOCATED_ID_MESSAGE = 'dropped a frame for a correlation id that this client never allocated'

function responsePlaintext(): Uint8Array {
  return toBinary(InnerMessageSchema, create(InnerMessageSchema, {
    kind: {
      case: 'response',
      value: create(InnerRpcResponseSchema, {
        payload: new Uint8Array([7]),
      }),
    },
  }))
}

// A frame whose correlation id has no handler is dropped either way. What these
// pin is the LEVEL: a late frame for an id this client retired on purpose (an
// aborted or timed-out call, a cancelled stream's end frame) is expected traffic,
// while an id this client never allocated is a protocol violation.
describe('ChannelInbound frames with no handler', () => {
  let capture: LoggerCapture
  beforeEach(() => {
    capture = captureLoggerOutput()
  })
  afterEach(() => capture.restore())

  // The counter stands at 5, so ids 1..4 were allocated and have since retired.
  function deliver(correlationId: number, flags = ChannelMessageFlags.UNSPECIFIED) {
    const ch = makeChannel({ nextRequestId: 5 })
    const { inbound } = makeInbound(ch)
    inbound.handleMessage('ch-1', encryptInner(ch, responsePlaintext(), correlationId, flags))
    return ch
  }

  it('logs a frame for a retired correlation id at debug level instead of warning', () => {
    deliver(4)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
  })

  it('treats the first id that a channel allocates as retired', () => {
    deliver(1)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
  })

  it('logs a MORE chunk for a retired correlation id at debug level and buffers nothing', () => {
    const ch = deliver(3, ChannelMessageFlags.MORE)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
    expect(ch.reassembly.size()).toBe(0)
  })

  it('warns for the id that the counter has not handed out yet', () => {
    deliver(5)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
    expect(capture.messages('debug', 'channel')).not.toContain(RETIRED_ID_MESSAGE)
  })

  it('warns for correlation id 0, which no request ever receives', () => {
    deliver(0)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
  })

  it('warns for the largest correlation id that the client still routes', () => {
    deliver(Number.MAX_SAFE_INTEGER)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
  })

  it('logs nothing about dropping a frame that has a live handler', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    const resolve = vi.fn()
    ch.pendingRequests.set(4, {
      resolve,
      reject: () => {},
    })
    const { inbound } = makeInbound(ch)
    inbound.handleMessage('ch-1', encryptInner(ch, responsePlaintext(), 4))
    expect(resolve).toHaveBeenCalledOnce()
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).not.toContain(RETIRED_ID_MESSAGE)
  })
})
