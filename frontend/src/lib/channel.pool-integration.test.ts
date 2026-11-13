import type { ChannelTransport } from './channel'
import { fromBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_MAX_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { InnerMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { WatchEventsRequestSchema } from '~/generated/proto/leapmux/v1/watch_pb'
import { blankClassicHandshakeState } from '~/test-support/channelSessionDoubles'
import { makeIdentitySession } from '~/test-support/identitySession'
import { WorkerWatchService } from '../generated/proto/leapmux/v1/watch_pb'
import {
  acceptingKeyPins,
  agePastHardCeiling,
  agePastMaxAge,
  AutoOpenMockWebSocket,
  ChannelManagerTestHarness,
  createMockTransport,
  decodeWireMessage,
  FIRST_TEST_REQUEST_ID,
  makeCountingTransport,
  makeIdentityCipherManager,
  makeMockTransport,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
  newTestChannelManager,
  sessions,
  sessionStateOf,
  settleChannelSessions,
  simulatePingAcceptOnWs,
  waitForPendingChannel,
} from './channel.test-support'

describe('ChannelManager getOrOpenChannel', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should return existing channel for same worker', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.mgr.getOrOpenChannel('w1')
    expect(ch2).toBe(ch1)
  })

  it('should open new channel for different worker', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2Promise = h.mgr.getOrOpenChannel('w2')
    await h.settle()
    await h.simulatePingAccept()
    const ch2 = await ch2Promise
    expect(ch2).not.toBe(ch1)
  })

  it('should open new channel if existing one is closed', async () => {
    const ch1 = await h.openTestChannel('w1')
    await h.mgr.closeChannel(ch1)
    const ch2Promise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulatePingAccept()
    const ch2 = await ch2Promise
    expect(ch2).not.toBe(ch1)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  // Age / soft-nonce use in-band rekey (same channel id); identity drift still
  // closes and reopens.
  it('rekeys a pooled channel that has aged past its max age without closing', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulateRekeyAck()
    const ch2 = await reusePromise
    expect(ch2).toBe(ch1)
    expect(h.mgr.isOpen(ch1)).toBe(true)
    const ch = sessionStateOf(ch1)
    expect(ch.lastRekeyAt).toBeGreaterThan(performance.now() - 5_000)
    expect(ch.session.send.nonce()).toBe(0)
    expect(ch.session.receive.nonce()).toBe(0)
  })

  it('rekeys a pooled channel whose send session needs a rekey without closing', async () => {
    const ch1 = await h.openTestChannel('w1')
    let soft = true
    sessionStateOf(ch1).session.send.needsRekey = () => soft
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    soft = false
    await h.simulateRekeyAck()
    const ch2 = await reusePromise
    expect(ch2).toBe(ch1)
    expect(h.mgr.isOpen(ch1)).toBe(true)
  })

  it('keeps the channel usable after a rate-limit RekeyReject', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulateRekeyReject()
    const ch2 = await reusePromise
    expect(ch2).toBe(ch1)
    expect(h.mgr.isOpen(ch1)).toBe(true)
    // Age-only retry suppressed until short reject backoff (monotonic).
    expect(sessionStateOf(ch1).rekeyNotBefore).toBeGreaterThan(performance.now())
  })

  it('honors retry_after_ms from RekeyReject for the backoff window', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    const before = performance.now()
    await h.simulateRekeyReject(h.mockWs, sessions, 120_000)
    await reusePromise
    const notBefore = sessionStateOf(ch1).rekeyNotBefore as number
    expect(notBefore).toBeGreaterThanOrEqual(before + 119_000)
    expect(notBefore).toBeLessThan(before + 121_000)
  })

  it('falls back to a one-minute backoff when RekeyReject omits retry_after_ms', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    const before = performance.now()
    await h.simulateRekeyReject() // retryAfterMs defaults to 0 / unset
    await reusePromise
    const notBefore = sessionStateOf(ch1).rekeyNotBefore as number
    expect(notBefore).toBeGreaterThanOrEqual(before + 59_000)
    expect(notBefore).toBeLessThan(before + 61_000)
  })

  it('closes a pooled channel past the hard ceiling when checkChannelsAfterWake runs', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastHardCeiling(sessionStateOf(ch1))
    h.mgr.checkChannelsAfterWake()
    await h.settle()
    expect(h.mgr.isOpen(ch1)).toBe(false)
  })

  it('does not re-initiate an age-only rekey while reject backoff is active', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch = sessionStateOf(ch1)
    agePastMaxAge(ch)
    const first = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulateRekeyReject()
    await first

    const sentBefore = h.mockWs.sent.length
    const second = await h.mgr.getOrOpenChannel('w1')
    expect(second).toBe(ch1)
    expect(h.mockWs.sent.length).toBe(sentBefore)
  })

  it('still initiates soft-nonce rekey while reject backoff is active', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch = sessionStateOf(ch1)
    agePastMaxAge(ch)
    const first = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulateRekeyReject()
    await first

    let soft = true
    ch.session.send.needsRekey = () => soft
    const sentBefore = h.mockWs.sent.length
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    expect(h.mockWs.sent.length).toBe(sentBefore + 1)
    soft = false
    await h.simulateRekeyAck()
    expect(await reusePromise).toBe(ch1)
    expect(ch.rekeyNotBefore).toBe(0)
  })

  it('shares one in-flight rekey across concurrent getOrOpenChannel callers', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const sentBefore = h.mockWs.sent.length
    const a = h.mgr.getOrOpenChannel('w1')
    const b = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    expect(h.mockWs.sent.length - sentBefore).toBe(1)
    await h.simulateRekeyAck()
    expect(await a).toBe(ch1)
    expect(await b).toBe(ch1)
  })

  it('reopens a pooled channel past the session-key hard ceiling', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastHardCeiling(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulatePingAccept()
    const ch2 = await reusePromise
    expect(ch2).not.toBe(ch1)
    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  it('closes and reopens after RekeyReject once past hard ceiling', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const reusePromise = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    // Age past the hard ceiling while waiting for Reject (slow peer).
    agePastHardCeiling(sessionStateOf(ch1))
    await h.simulateRekeyReject()
    // The reject ends ch1's session, and the pool then opens a new channel,
    // whose verification Ping is on the wire once the sessions settle.
    expect(decodeWireMessage(h.mockWs.sent.at(-1)!).channelId).not.toBe(ch1)
    await h.simulatePingAccept()
    const ch2 = await reusePromise
    expect(ch2).not.toBe(ch1)
    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  it('rejects call when pooled channel is past hard ceiling', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastHardCeiling(sessionStateOf(ch1))
    await expect(h.mgr.call(ch1, 'Echo', new Uint8Array([1]))).rejects.toThrow(/hard ceiling/)
    expect(h.mgr.isOpen(ch1)).toBe(false)
  })

  it('rejects call past hard ceiling even during reject backoff', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch = sessionStateOf(ch1)
    agePastHardCeiling(ch)
    // Active backoff would make shouldInitiateRekey false; ceiling must still win.
    ch.rekeyNotBefore = performance.now() + 60_000
    await expect(h.mgr.call(ch1, 'Echo', new Uint8Array([1]))).rejects.toThrow(/hard ceiling/)
    expect(h.mgr.isOpen(ch1)).toBe(false)
  })

  it('delivers stream rekey failure to onError after the handle returns', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastHardCeiling(sessionStateOf(ch1))
    const errorFn = vi.fn()
    const endFn = vi.fn()
    const handle = h.mgr.stream(ch1, WorkerWatchService.method.watchEvents, {})
    handle.onError(errorFn)
    handle.onEnd(endFn)
    expect(errorFn).not.toHaveBeenCalled()
    await h.settle()
    expect(errorFn).toHaveBeenCalledTimes(1)
    expect(errorFn.mock.calls[0]?.[0].message).toMatch(/hard ceiling/)
    expect(endFn).not.toHaveBeenCalled()
    expect(h.mgr.isOpen(ch1)).toBe(false)
  })

  it('rekeys before a stream send when the pooled channel has aged', async () => {
    const ch1 = await h.openTestChannel('w1')
    const handle = h.mgr.stream(ch1, WorkerWatchService.method.watchEvents, {})
    await h.settle()
    // StreamOpen advanced the initiator send nonce; consume it on the
    // responder so simulateRekeyAck can decrypt the subsequent RekeyRequest.
    const pair = sessions.get(ch1)!
    pair.responder.receive.decrypt(decodeWireMessage(h.mockWs.sent.at(-1)!).ciphertext)
    agePastMaxAge(sessionStateOf(ch1))
    const sentBefore = h.mockWs.sent.length
    handle.send({ updateId: 9n })
    await h.settle()
    // Only the RekeyRequest is on the wire: the update waits in the session
    // worker. simulateRekeyAck fails unless that last frame is a RekeyRequest.
    expect(h.mockWs.sent.length).toBe(sentBefore + 1)
    await h.simulateRekeyAck()
    expect(h.mockWs.sent.length).toBe(sentBefore + 2)
    const sentMsg = decodeWireMessage(h.mockWs.sent.at(-1)!)
    expect(Number(sentMsg.correlationId)).toBe(handle.requestId)
    const update = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    if (update.kind.case !== 'streamRequest' || update.kind.value.action.case !== 'payload')
      throw new Error('the update must reach the wire as a stream revision')
    expect(fromBinary(WatchEventsRequestSchema, update.kind.value.action.value).updateId).toBe(9n)
  })

  it('rekeys before call when the pooled channel has aged', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const callPromise = h.mgr.call(ch1, 'Echo', new Uint8Array([7]))
    await h.settle()
    await h.simulateRekeyAck()
    await h.settle()
    const echoWire = decodeWireMessage(h.mockWs.sent.at(-1)!)
    await h.sendResponseFromWorker(ch1, Number(echoWire.correlationId), new Uint8Array([9]))
    const resp = await callPromise
    expect(resp).toEqual(new Uint8Array([9]))
    expect(h.mgr.isOpen(ch1)).toBe(true)
  })

  it('closes the channel when rekey Ack/Reject never arrives', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    try {
      const ch1 = await h.openTestChannel('w1')
      agePastMaxAge(sessionStateOf(ch1))
      const reusePromise = h.mgr.getOrOpenChannel('w1')
      await h.settle()
      await vi.advanceTimersByTimeAsync(10_000)
      await h.settle()
      // Timeout closes the aged channel; getOrOpenChannel falls through and
      // opens a fresh one (transparent to the caller).
      await h.simulatePingAccept()
      const ch2 = await reusePromise
      expect(ch2).not.toBe(ch1)
      expect(h.mgr.isOpen(ch1)).toBe(false)
      expect(h.mgr.isOpen(ch2)).toBe(true)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('rejects call when AbortSignal fires during an in-flight rekey, and never sends it', async () => {
    const ch1 = await h.openTestChannel('w1')
    agePastMaxAge(sessionStateOf(ch1))
    const controller = new AbortController()
    const callPromise = h.mgr.call(ch1, 'Echo', new Uint8Array([1]), undefined, controller.signal)
    const rejected = expect(callPromise).rejects.toThrow('navigated away')
    await h.settle()
    controller.abort(new Error('navigated away'))
    await h.simulateRekeyAck()
    await h.settle()
    await rejected
    // The open's Ping took the first id, so the Echo took the next one. Its
    // starting send waited for the rekey, and the abort released it there.
    const echoFrames = h.mockWs.sent
      .map(frame => decodeWireMessage(frame))
      .filter(message => message.channelId === ch1 && Number(message.correlationId) === FIRST_TEST_REQUEST_ID)
    expect(echoFrames).toEqual([])
  })

  it('reopens a pooled channel whose hub-authenticated identity has drifted', async () => {
    sessions.clear()
    const driftWs = new MockWebSocket()
    let hubUserId = 'user-a'
    let expected: string | undefined = 'user-a'
    const base = createMockTransport(driftWs)
    const transport: ChannelTransport = {
      ...base,
      async openChannel(workerId: string, handshakePayload: Uint8Array) {
        const r = await base.openChannel(workerId, handshakePayload)
        return { ...r, userId: hubUserId }
      },
    }
    const driftMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      expectedUserId: () => expected,
    })
    try {
      // Open the pooled channel as user-a (expectation matches).
      const openPromise = driftMgr.openChannel('w1')
      await h.settle()
      driftWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(driftWs)
      const ch1 = await openPromise

      // This tab logs out and back in as user-b; the Hub authenticates the reopen
      // as user-b too. The pooled user-a channel must be rotated out, not reused.
      hubUserId = 'user-b'
      expected = 'user-b'
      const ch2Promise = driftMgr.getOrOpenChannel('w1')
      await h.settle()
      await h.simulatePingAccept(driftWs)
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
      expect((driftMgr as any).channels.get(ch2).userId).toBe('user-b')
    }
    finally {
      driftMgr.closeAll()
    }
  })
})

// ---------------------------------------------------------------------------
// getOrOpenChannel concurrency / deduplication
//
// These tests exercise *concurrent* opens, which the main suite's real-crypto
// mock handshake cannot model (mockHandshake2 returns the LAST registered
// session, so two simultaneous opens would clobber each other's crypto state).
// They use a self-contained identity-cipher setup so multiple channels can be
// opened at once without real crypto.
// ---------------------------------------------------------------------------
describe('ChannelManager getOrOpenChannel deduplication', () => {
  it('should return the same channel for concurrent calls to the same worker', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    // Each openChannel call gets a unique channel ID so we can detect duplicates.
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
      // The Hub always names the authenticated user; openChannel rejects without it.
      userId: 'user-1',
      maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
    }))

    const cm = newTestChannelManager(transport, {
      session: {
        classicHandshake1: (_rs: Uint8Array) => ({
          message1: new Uint8Array(48),
          handshakeState: blankClassicHandshakeState(),
        }),
        classicHandshake2: () => makeIdentitySession(),
      },
      keyPins: acceptingKeyPins(),
    })

    // Launch two concurrent getOrOpenChannel calls for the same worker.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-1')

    // Let the handshake + WebSocket open progress.
    await settleChannelSessions()
    // The single deduplicated open still verifies its session with a Ping.
    await simulatePingAcceptOnWs(ws!, 'ch-1')

    const [ch1, ch2] = await Promise.all([p1, p2])

    // Both should resolve to the same channel — only one openChannel call.
    expect(ch1).toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(1)

    cm.closeAll()
  })

  it('should open separate channels for different workers', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
      // The Hub always names the authenticated user; openChannel rejects without it.
      userId: 'user-1',
      maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
    }))

    const cm = newTestChannelManager(transport, {
      session: {
        classicHandshake1: (_rs: Uint8Array) => ({
          message1: new Uint8Array(48),
          handshakeState: blankClassicHandshakeState(),
        }),
        classicHandshake2: () => makeIdentitySession(),
      },
      keyPins: acceptingKeyPins(),
    })

    // Launch concurrent getOrOpenChannel calls for different workers.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-2')

    await settleChannelSessions()
    // Each open verifies its own session with a Ping.
    await simulatePingAcceptOnWs(ws!, 'ch-1')
    await simulatePingAcceptOnWs(ws!, 'ch-2')

    const [ch1, ch2] = await Promise.all([p1, p2])

    expect(ch1).not.toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(2)

    cm.closeAll()
  })
})

describe('ChannelManager pooled channel identity', () => {
  // A pooled channel carries the identity the Hub authenticated its OPEN as, for up to
  // an hour. On a shared machine a tab logs out and back in as B; without this check
  // every worker RPC B's page issues would keep running on the worker AS A, because the
  // pool only ever looked at age and rekey.
  it('does not reuse a pooled channel after the expected identity changes', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let identity = 'user-a'
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => identity)
    const cm = makeIdentityCipherManager(transport, {
      expectedUserId: () => identity,
    })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')

      // Same tab, same manager, different user.
      identity = 'user-b'

      const second = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-2')
      await simulatePingAcceptOnWs(ws!, 'ch-2')

      expect(await second).toBe('ch-2')
      expect(cm.isOpen('ch-1')).toBe(false)
      expect(transport.openChannel).toHaveBeenCalledTimes(2)
    }
    finally {
      cm.closeAll()
    }
  })

  // hasOpenChannel drives the worker "connected" indicator, and it must agree with the
  // reuse path on identity drift: a pooled channel authenticated as a DIFFERENT user
  // is one getOrOpenChannel would reject and reopen, so reporting "connected" for it
  // would claim a live link the current user cannot use as themselves. (Age / rekey
  // are deliberately NOT excluded here -- their rotation is transparent.)
  it('reports a drifted-identity channel as not open from hasOpenChannel', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let identity = 'user-a'
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => identity)
    const cm = makeIdentityCipherManager(transport, {
      expectedUserId: () => identity,
    })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')
      expect(cm.hasOpenChannel('worker-1')).toBe(true)

      // Same tab, same manager, different user: the pooled channel is still open but
      // authenticated as user-a, so it no longer counts as connected for user-b.
      identity = 'user-b'
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
    }
    finally {
      cm.closeAll()
    }
  })

  // The check must not churn the pool when nothing changed.
  it('reuses a pooled channel while the expected identity is unchanged', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => 'user-a')
    const cm = makeIdentityCipherManager(transport, {
      expectedUserId: () => 'user-a',
    })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')

      expect(await cm.getOrOpenChannel('worker-1')).toBe('ch-1')
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      cm.closeAll()
    }
  })
})
