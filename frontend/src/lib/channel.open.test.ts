import type { ChannelSocket, ChannelTransport, WorkerKeyBundle } from './channel'
import type { KeyPinDecision } from './keyPinStore'
import type { Session } from './noise'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcResponseSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { KEY_KEY_PINS, localStorageGet } from './browserStorage'
import { ChannelManager } from './channel'
import {
  acceptingKeyPins,
  AutoOpenMockWebSocket,
  channelInternals,
  ChannelManagerTestHarness,
  clearAllKeyPins,
  createMockTransport,
  createTestSession,
  encodeWireMessage,
  makeCountingTransport,
  makeIdentityCipherManager,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
  sessions,
  settle,
  simulatePingAcceptOnWs,
  simulatePingErrorOnWs,
  waitForPendingChannel,
} from './channel.test-support'
import { KeyPinStore } from './keyPinStore'
import {
  DEFAULT_MAX_MESSAGE_SIZE,
  INNER_ENVELOPE_HEADROOM,
  MAX_CHUNK_SIZE,
  MAX_CONFIGURABLE_MESSAGE_SIZE,
  maxReassembledMessageSize,
} from './reassembler'

describe('channelManager openChannel', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should open a channel and return the channel ID', async () => {
    const channelId = await h.openTestChannel('w1')
    expect(channelId).toBeTruthy()
    expect(h.mgr.isOpen(channelId)).toBe(true)
    expect(channelInternals(h.mgr, channelId).workerId).toBe('w1')
  })

  it('wraps unexpected key-pin errors as ChannelError', async () => {
    const pins = new KeyPinStore({
      confirmKeyPin: async () => {
        throw new Error('dialog boom')
      },
    })
    // Seed a pin so resolve takes the mismatch path and hits confirmKeyPin.
    ;(await acceptingKeyPins().resolve('w-pin-err', {
      x25519PublicKey: new Uint8Array(32).fill(1),
      mlkemPublicKey: new Uint8Array(0),
      slhdsaPublicKey: new Uint8Array(0),
    }))()
    const boomMgr = new ChannelManager(createMockTransport(new MockWebSocket()), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      keyPins: pins,
    })
    try {
      // Force mismatch: mock transport keys differ from the seeded pin.
      await expect(boomMgr.openChannel('w-pin-err')).rejects.toMatchObject({
        name: 'ChannelError',
        source: 'client',
        message: 'dialog boom',
      })
    }
    finally {
      boomMgr.closeAll()
    }
  })

  it('wraps KeyPinRejectedError as a client ChannelError', async () => {
    const pins = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    ;(await acceptingKeyPins().resolve('w-pin-rej', {
      x25519PublicKey: new Uint8Array(32).fill(1),
      mlkemPublicKey: new Uint8Array(0),
      slhdsaPublicKey: new Uint8Array(0),
    }))()
    const rejectMgr = new ChannelManager(createMockTransport(new MockWebSocket()), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      keyPins: pins,
    })
    try {
      await expect(rejectMgr.openChannel('w-pin-rej')).rejects.toMatchObject({
        name: 'ChannelError',
        source: 'client',
        message: 'Worker public key rejected by user',
      })
    }
    finally {
      rejectMgr.closeAll()
    }
  })

  it('should reuse the WebSocket for multiple channels', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')
    expect(ch1).not.toBe(ch2)
    expect(h.mgr.isOpen(ch1)).toBe(true)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  // A failed open must tell the Hub to drop the channel it already registered.
  //
  // transport.openChannel has returned by the time the Ping runs, so the Hub holds a
  // registered channel and the Worker a live Noise session. Without a rollback, a
  // retry loop against a flaky relay strands one of each per attempt -- consuming
  // the Worker's per-channel caps -- until the credential is revoked or the process
  // restarts. The Go client of this protocol rolls back at exactly this boundary
  // (backend/tunnel/channel.go's rollbackRegisteredChannel).
  it('tells the Hub to close the channel when the session ping fails', async () => {
    const brokenWs = new MockWebSocket()
    const base = createMockTransport(brokenWs)
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const brokenMgr = new ChannelManager(transport, {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      rpcTimeoutFn: () => 20,
    })
    try {
      const openPromise = brokenMgr.openChannel('w1')
      await h.flushMicrotasks()
      brokenWs.simulateOpen()
      await h.flushMicrotasks()
      // The worker never answers the ping.
      await expect(openPromise).rejects.toThrow()
      expect(closed).toHaveLength(1)
    }
    finally {
      brokenMgr.closeAll()
    }
  })

  // Same for the empty-userId rejection: it also fires AFTER openChannel returned.
  it('tells the Hub to close the channel when it omits the authenticated user id', async () => {
    const omittedWs = new MockWebSocket()
    const base = createMockTransport(omittedWs)
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async openChannel(workerId, handshakePayload) {
        const r = await base.openChannel(workerId, handshakePayload)
        return { ...r, userId: '' }
      },
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const omittedMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      await expect(omittedMgr.openChannel('w1')).rejects.toThrow(/empty authenticated user id/)
      expect(closed).toHaveLength(1)
    }
    finally {
      omittedMgr.closeAll()
    }
  })

  // Clients adopt the negotiated payload budget; they do not guess a default.
  // A missing announcement (old Hub) or an out-of-bounds one must refuse the
  // open and roll the Hub registration back — same boundary as an empty user id.
  it('tells the Hub to close the channel when max_message_size is missing', async () => {
    const omittedWs = new MockWebSocket()
    const base = createMockTransport(omittedWs, { maxMessageSize: false })
    const closed: string[] = []
    const handshake2 = vi.fn(mockHandshake2)
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const omittedMgr = new ChannelManager(transport, {
      handshake1: mockHandshake1,
      handshake2,
    })
    try {
      await expect(omittedMgr.openChannel('w1')).rejects.toThrow(/no max_message_size/)
      expect(closed).toHaveLength(1)
      // Limits are validated before handshake-2 so a missing announcement
      // does not pay Noise finish cost (matches Go tunnel ordering).
      expect(handshake2).not.toHaveBeenCalled()
    }
    finally {
      omittedMgr.closeAll()
    }
  })

  it('tells the Hub to close the channel when max_message_size is out of bounds', async () => {
    const badWs = new MockWebSocket()
    const base = createMockTransport(badWs, { maxMessageSize: 1 })
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const badMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      await expect(badMgr.openChannel('w1')).rejects.toThrow(/max_message_size 1 out of bounds/)
      expect(closed).toHaveLength(1)
    }
    finally {
      badMgr.closeAll()
    }
  })

  it('tells the Hub to close the channel when max_message_size is above the configurable ceiling', async () => {
    const badWs = new MockWebSocket()
    const tooLarge = MAX_CONFIGURABLE_MESSAGE_SIZE + 1
    const base = createMockTransport(badWs, { maxMessageSize: tooLarge })
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const badMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      await expect(badMgr.openChannel('w1')).rejects.toThrow(new RegExp(`max_message_size ${tooLarge} out of bounds`))
      expect(closed).toHaveLength(1)
    }
    finally {
      badMgr.closeAll()
    }
  })

  it('tells the Hub to close the channel when max_message_size is zero', async () => {
    const zeroWs = new MockWebSocket()
    const base = createMockTransport(zeroWs, { maxMessageSize: 0 })
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const zeroMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      await expect(zeroMgr.openChannel('w1')).rejects.toThrow(/no max_message_size/)
      expect(closed).toHaveLength(1)
    }
    finally {
      zeroMgr.closeAll()
    }
  })

  it('adopts negotiated maxMessageSize and derives the reassembled ceiling', async () => {
    const negotiated = DEFAULT_MAX_MESSAGE_SIZE
    const channelId = await h.openTestChannel('w1')
    const ch = (h.mgr as any).channels.get(channelId)
    expect(ch.maxMessageSize).toBe(negotiated)
    expect(ch.maxReassembledMessageSize).toBe(maxReassembledMessageSize(negotiated))
    expect(ch.reassembly).toBeTruthy()
  })

  it('adopts a non-default negotiated maxMessageSize and derives reassembled headroom', async () => {
    const negotiated = 1 << 20
    const customWs = new MockWebSocket()
    const customMgr = new ChannelManager(createMockTransport(customWs, { maxMessageSize: negotiated }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
    })
    try {
      const openPromise = customMgr.openChannel('w1')
      await h.flushMicrotasks()
      customWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(customWs)
      const channelId = await openPromise
      const ch = (customMgr as any).channels.get(channelId)
      expect(ch.maxMessageSize).toBe(negotiated)
      expect(ch.maxReassembledMessageSize).toBe(maxReassembledMessageSize(negotiated))
      expect(ch.maxReassembledMessageSize).toBe(negotiated + INNER_ENVELOPE_HEADROOM)
    }
    finally {
      customMgr.closeAll()
    }
  })

  it('accepts negotiated maxMessageSize exactly at MAX_CHUNK_SIZE', async () => {
    const floorWs = new MockWebSocket()
    const floorMgr = new ChannelManager(createMockTransport(floorWs, { maxMessageSize: MAX_CHUNK_SIZE }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
    })
    try {
      const openPromise = floorMgr.openChannel('w1')
      await h.flushMicrotasks()
      floorWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(floorWs)
      const channelId = await openPromise
      const ch = (floorMgr as any).channels.get(channelId)
      expect(ch.maxMessageSize).toBe(MAX_CHUNK_SIZE)
      expect(ch.maxReassembledMessageSize).toBe(maxReassembledMessageSize(MAX_CHUNK_SIZE))
    }
    finally {
      floorMgr.closeAll()
    }
  })

  it('derives reassembled headroom from testPayloadBudget when ceiling is omitted', async () => {
    const payload = 1 << 20
    const omitWs = new MockWebSocket()
    const omitMgr = new ChannelManager(createMockTransport(omitWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: payload,
    })
    try {
      const openPromise = omitMgr.openChannel('w1')
      await h.flushMicrotasks()
      omitWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(omitWs)
      const channelId = await openPromise
      const ch = (omitMgr as any).channels.get(channelId)
      expect(ch.maxMessageSize).toBe(payload)
      expect(ch.maxReassembledMessageSize).toBe(maxReassembledMessageSize(payload))
      expect(ch.maxReassembledMessageSize).toBe(payload + INNER_ENVELOPE_HEADROOM)
    }
    finally {
      omitMgr.closeAll()
    }
  })

  it('defaults payload to testReassembledCeiling when only the ceiling is set', async () => {
    const ceiling = 50
    const ceilWs = new MockWebSocket()
    const ceilMgr = new ChannelManager(createMockTransport(ceilWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testReassembledCeiling: ceiling,
    })
    try {
      const openPromise = ceilMgr.openChannel('w1')
      await h.flushMicrotasks()
      ceilWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(ceilWs)
      const channelId = await openPromise
      const ch = (ceilMgr as any).channels.get(channelId)
      expect(ch.maxMessageSize).toBe(ceiling)
      expect(ch.maxReassembledMessageSize).toBe(ceiling)
    }
    finally {
      ceilMgr.closeAll()
    }
  })

  // Handshake-2 verification also runs AFTER the Hub registered the channel, so
  // its failure must roll the registration back exactly like the ping and
  // identity failures: a forged or corrupted handshake-2 (wrong length, bad AEAD
  // tag, invalid SLH-DSA signature) otherwise strands a Hub-registered channel
  // and a live Worker session per retry against a misbehaving worker. The Go
  // client covers the same step under its rollback defer
  // (backend/tunnel/channel.go's handshaker.finish).
  it('tells the Hub to close the channel when handshake-2 verification fails', async () => {
    const ws = new MockWebSocket()
    const base = createMockTransport(ws)
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const failingMgr = new ChannelManager(transport, {
      handshake1: mockHandshake1,
      handshake2: () => {
        throw new Error('handshake message 2 failed to verify')
      },
    })
    try {
      await expect(failingMgr.openChannel('w1')).rejects.toThrow(/failed to verify/)
      expect(closed).toHaveLength(1)
      expect(failingMgr.hasOpenChannel('w1')).toBe(false)
    }
    finally {
      failingMgr.closeAll()
    }
  })

  // A throw AFTER the channel entered the pool as verified (today only a
  // hypothetical commitPin failure -- browserStorage swallows write errors -- but
  // the exit must stay safe if that ever changes) must evict the channel as well
  // as roll the Hub registration back: a verified ghost left in the pool would be
  // served by getOrOpenChannel for up to an hour while every RPC on it times out.
  it('evicts the pooled channel when a post-verification step throws', async () => {
    const ws = new MockWebSocket()
    const base = createMockTransport(ws)
    const closed: string[] = []
    const transport: ChannelTransport = {
      ...base,
      async closeChannel(channelId) {
        closed.push(channelId)
      },
    }
    const throwingMgr = new ChannelManager(transport, {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      keyPins: {
        resolve: async () => () => {
          throw new Error('pin store rejected the write')
        },
      } as unknown as KeyPinStore,
    })
    try {
      const openPromise = throwingMgr.openChannel('w1')
      await h.flushMicrotasks()
      ws.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(ws)
      await expect(openPromise).rejects.toThrow(/pin store rejected the write/)
      // Not a ghost: the pool must not serve it, and the Hub was told to drop it.
      expect(throwingMgr.hasOpenChannel('w1')).toBe(false)
      expect(closed).toHaveLength(1)
    }
    finally {
      throwingMgr.closeAll()
    }
  })

  // The open must not hand back a channel whose session does not actually work.
  // Noise_NK's handshake only proves THIS side can encrypt to the worker's
  // static key -- nothing in it proves the worker decrypts, or that its replies
  // decrypt back. Without the open-time Ping round trip, a session broken in
  // either direction opened "successfully" and failed on the caller's first real
  // call, and getOrOpenChannel would have cached the broken channel and served it
  // to every later caller.
  it('rejects the open when the session cannot round-trip a ping', async () => {
    const brokenWs = new MockWebSocket()
    const brokenMgr = new ChannelManager(createMockTransport(brokenWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      rpcTimeoutFn: () => 20,
    })
    try {
      const openPromise = brokenMgr.openChannel('w1')
      await h.flushMicrotasks()
      brokenWs.simulateOpen()
      await h.flushMicrotasks()
      // The worker never answers the ping: the session is dead in at least one
      // direction. The open must fail rather than cache a broken channel.
      await expect(openPromise).rejects.toThrow()
      expect(brokenMgr.isOpen('ch-1')).toBe(false)
    }
    finally {
      brokenMgr.closeAll()
    }
  })

  // A Hub response with no authenticated user id must be REJECTED, not quietly
  // replaced with a locally-asserted identity. Falling back re-opened the exact
  // hole the Hub-authenticated claim closes: a stale local auth store (an
  // account or impersonation switch) could self-assert an identity the Worker
  // would reject. The Go client of this protocol rejects at the same boundary.
  it('rejects the open when the Hub omits the authenticated user id', async () => {
    const omittedWs = new MockWebSocket()
    const base = createMockTransport(omittedWs)
    const transport: ChannelTransport = {
      ...base,
      async openChannel(workerId, handshakePayload) {
        const r = await base.openChannel(workerId, handshakePayload)
        return { ...r, userId: '' } // Hub omitted the identity
      },
    }
    const omittedMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      await expect(omittedMgr.openChannel('w1')).rejects.toThrow(/empty authenticated user id/)
      // No claim may be asserted at all -- not a local id, not an empty one:
      // the open is abandoned before anything reaches the wire.
      expect(omittedWs.sent).toHaveLength(0)
    }
    finally {
      omittedMgr.closeAll()
    }
  })

  // A Hub answer that disagrees with who this page thinks it is must FAIL the
  // open, not be taken silently.
  //
  // Validating the identity and then discarding it leaves the two free to
  // diverge: a tab rendered as A whose shared cookie jar has since been
  // re-authenticated as B (a logout/login in another tab, an impersonation
  // switch) opens a channel the Hub authenticates as B, and A's UI then drives
  // B's session on every worker B can reach. The Hub stays authoritative --
  // nothing here overrides it -- the open just refuses to proceed on a
  // disagreement the page cannot otherwise see.
  it('rejects the open when the Hub authenticates a different user than expected', async () => {
    const divergedWs = new MockWebSocket()
    const mgr = new ChannelManager(createMockTransport(divergedWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      // The mock Hub authenticates every open as 'hub-authenticated-user'.
      expectedUserId: () => 'stale-user-a',
    })
    try {
      await expect(mgr.openChannel('w1')).rejects.toThrow(/authenticated this channel as hub-authenticated-user, not the expected stale-user-a/)
      // Abandoned before anything reaches the wire.
      expect(divergedWs.sent).toHaveLength(0)
    }
    finally {
      mgr.closeAll()
    }
  })

  // An EMPTY-STRING expected identity is a degenerate/corrupt id, NOT "no
  // expectation yet" (undefined). It disagrees with the Hub's real answer, so the
  // open must be refused: treating '' as "no opinion" (the falsy `!!expected` trap)
  // would silently serve a channel bound to a different, non-empty user.
  it('rejects the open when the expected identity is a degenerate empty string', async () => {
    const emptyWs = new MockWebSocket()
    const mgr = new ChannelManager(createMockTransport(emptyWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      expectedUserId: () => '',
    })
    try {
      await expect(mgr.openChannel('w1')).rejects.toThrow(/authenticated this channel as hub-authenticated-user/)
      expect(emptyWs.sent).toHaveLength(0)
    }
    finally {
      mgr.closeAll()
    }
  })

  // A page with no expectation yet (auth still resolving) must not be blocked:
  // undefined means "no opinion", which is different from "expects nobody".
  it('opens normally when the page has no expected identity yet', async () => {
    sessions.clear()
    const mgr = new ChannelManager(createMockTransport(h.mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      expectedUserId: () => undefined,
    })
    try {
      const openPromise = mgr.openChannel('w1')
      await h.flushMicrotasks()
      h.mockWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept()
      await expect(openPromise).resolves.toBeTruthy()
    }
    finally {
      mgr.closeAll()
    }
  })

  // The matching case must proceed — the check only fires on a real disagreement.
  it('opens when the Hub agrees with the expected identity', async () => {
    sessions.clear()
    const mgr = new ChannelManager(createMockTransport(h.mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      expectedUserId: () => 'hub-authenticated-user',
    })
    try {
      const openPromise = mgr.openChannel('w1')
      await h.flushMicrotasks()
      h.mockWs.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept()
      await expect(openPromise).resolves.toBeTruthy()
    }
    finally {
      mgr.closeAll()
    }
  })
})

describe('channelManager encryption modes', () => {
  const h = new ChannelManagerTestHarness()

  it('should open a channel with classic encryption (X25519 only)', async () => {
    // Create a transport that returns CLASSIC mode.
    const classicWs = new MockWebSocket()
    const classicSessions = new Map<string, { initiator: Session, responder: Session }>()

    const classicTransport: ChannelTransport = {
      async getWorkerHandshakeParams(_workerId: string): Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }> {
        return {
          keys: {
            x25519PublicKey: new Uint8Array(32),
            mlkemPublicKey: new Uint8Array(0),
            slhdsaPublicKey: new Uint8Array(0),
          },
          encryptionMode: EncryptionMode.CLASSIC,
        }
      },
      async openChannel(_workerId: string, _handshakePayload: Uint8Array) {
        const channelId = `ch-classic-${Math.random().toString(36).slice(2, 8)}`
        const pair = createTestSession()
        classicSessions.set(channelId, pair)
        return { channelId, handshakePayload: new Uint8Array(48), userId: 'hub-authenticated-user', maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE }
      },
      async closeChannel(_channelId: string) {},
      createWebSocket(): ChannelSocket {
        return classicWs
      },
    }

    // Mock classic handshake functions.
    function mockClassicHS1(_remoteX25519Pub: Uint8Array) {
      return {
        handshakeState: {} as any,
        message1: new Uint8Array(48),
      }
    }

    function mockClassicHS2(_state: any, _message2: Uint8Array) {
      const entries = [...classicSessions.entries()]
      const lastEntry = entries.at(-1)
      if (!lastEntry)
        throw new Error('No session registered')
      return lastEntry[1].initiator
    }

    const classicMgr = new ChannelManager(classicTransport, {
      classicHandshake1: mockClassicHS1,
      classicHandshake2: mockClassicHS2,
      keyPins: acceptingKeyPins(),
    })

    const openPromise = classicMgr.openChannel('w-classic')
    await h.flushMicrotasks()
    classicWs.readyState = MockWebSocket.OPEN
    classicWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept(classicWs, classicSessions)

    const channelId = await openPromise
    const pair = classicSessions.get(channelId)!
    expect(classicMgr.isOpen(channelId)).toBe(true)

    // Verify a call works through the encrypted channel.
    const callPromise = classicMgr.call(channelId, 'TestMethod', new Uint8Array([1, 2]))

    const resp = create(InnerRpcResponseSchema, {
      payload: new Uint8Array([3, 4]),
      isError: false,
    })
    const respEnv = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const respPt = toBinary(InnerMessageSchema, respEnv)
    const respCt = pair.responder.send.encrypt(respPt)
    // The open's session-verifying Ping consumed correlation id 1.
    classicWs.simulateMessage(encodeWireMessage(channelId, respCt, { id: 2 }))

    const result = await callPromise
    expect(result.payload).toEqual(new Uint8Array([3, 4]))

    classicMgr.closeAll()
  })
})

describe('channelManager open-time verification', () => {
  // The whole point of the open-time Ping is that a session broken in either direction
  // never reaches a caller -- and channels are POOLED, so "a caller" includes everyone
  // who asks for this worker while the ping is still in flight. The channel has to be
  // in the manager's map for the ping's own reply to route, so presence in the map
  // cannot be what makes it available.
  it('does not hand out a channel whose verification ping is still in flight', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')

      // The ping is on the wire: the channel exists but is unproven.
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
      expect(channelInternals(cm, 'ch-1').state).toBe('opening')

      let racerResult: string | null = null
      const racer = cm.getOrOpenChannel('worker-1').then((id) => {
        racerResult = id
        return id
      })
      await settle()

      // The racer must be waiting on the SAME open, not holding the unproven channel.
      expect(racerResult).toBeNull()
      expect(transport.openChannel).toHaveBeenCalledTimes(1)

      simulatePingAcceptOnWs(ws!, 'ch-1')

      expect(await open).toBe('ch-1')
      expect(await racer).toBe('ch-1')
      expect(cm.hasOpenChannel('worker-1')).toBe(true)
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      cm.closeAll()
    }
  })

  // The failure case is the one that mattered: a racer handed the unverified channel
  // was left holding an id the open then deleted locally and rolled back at the Hub.
  it('rejects a racing getOrOpenChannel when the verification ping fails', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      const racer = cm.getOrOpenChannel('worker-1')
      await settle()

      // Attach rejection handlers BEFORE delivering the error — bun treats an
      // unhandled pending.reject as a test failure. Do not wrap the expects in
      // Promise.all before simulate: that would await the ping timeout first.
      let openErr: unknown
      let racerErr: unknown
      const openDone = open.then(
        () => { throw new Error('open should have rejected') },
        (e: unknown) => { openErr = e },
      )
      const racerDone = racer.then(
        () => { throw new Error('racer should have rejected') },
        (e: unknown) => { racerErr = e },
      )
      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await Promise.all([openDone, racerDone])
      expect(String(openErr)).toContain('session is dead')
      expect(String(racerErr)).toContain('session is dead')
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
      expect(cm.isOpen('ch-1')).toBe(false)
      // The Hub-side registration was rolled back.
      expect(transport.closeChannel).toHaveBeenCalledWith('ch-1')
    }
    finally {
      cm.closeAll()
    }
  })

  // A failed open deletes the channel from the map, which puts it beyond the reach of
  // closeChannel and of the WebSocket teardown -- so anything registered on it has to
  // be settled right here or it is settled by nothing at all, and waits out its own
  // 15s RPC timeout on a channel that no longer exists.
  it('rejects requests registered on the channel while its verification ping was in flight', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')

      // call() still permits 'opening' so the verification Ping can complete;
      // stream() requires verified and must reject here.
      const raced = cm.call('ch-1', 'Test', new Uint8Array())
      expect(() => cm.stream('ch-1', 'WatchEvents', new Uint8Array())).toThrow('channel not open')

      let openErr: unknown
      let racedErr: unknown
      const openDone = open.then(
        () => { throw new Error('open should have rejected') },
        (e: unknown) => { openErr = e },
      )
      const racedDone = raced.then(
        () => { throw new Error('raced call should have rejected') },
        (e: unknown) => { racedErr = e },
      )
      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await Promise.all([openDone, racedDone])
      expect(String(openErr)).toContain('session is dead')
      expect(String(racedErr)).toContain('session is dead')
    }
    finally {
      cm.closeAll()
    }
  })
})

describe('channelManager key pinning across concurrent opens', () => {
  beforeEach(() => {
    clearAllKeyPins()
  })

  afterEach(() => {
    clearAllKeyPins()
  })

  /** A transport whose worker keys are per-worker and mutable, so a key can be rotated mid-test. */
  function makeKeyedTransport(onCreateWs: () => AutoOpenMockWebSocket, keyByWorker: Map<string, number>): ChannelTransport {
    const transport = makeCountingTransport(onCreateWs)
    transport.getWorkerHandshakeParams = vi.fn().mockImplementation(async (workerId: string) => ({
      keys: {
        x25519PublicKey: new Uint8Array(32).fill(keyByWorker.get(workerId)!),
        mlkemPublicKey: new Uint8Array(0),
        slhdsaPublicKey: new Uint8Array(0),
      } satisfies WorkerKeyBundle,
      encryptionMode: EncryptionMode.CLASSIC,
    }))
    return transport
  }

  // KEY_KEY_PINS holds EVERY worker's pin in one value, and opens to different workers
  // are not serialized (the open dedup is keyed by worker). Reading the map before the
  // prompt/handshake/WebSocket awaits and writing it back after made each open an
  // unserialized read-modify-write: the later writer's snapshot predated the earlier
  // one's pin, so it silently erased it.
  it('keeps both pins when opens to different workers interleave', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const p1 = cm.getOrOpenChannel('w1')
      const p2 = cm.getOrOpenChannel('w2')
      await waitForPendingChannel(cm, 'ch-1')
      await waitForPendingChannel(cm, 'ch-2')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])

      expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).sort()).toEqual(['w1', 'w2'])
    }
    finally {
      cm.closeAll()
    }
  })

  // Why the lost pin is a security bug and not a lost preference: with w1's pin gone,
  // the next open reads no pin at all, takes the FIRST-USE branch, and silently pins
  // whatever key the Hub serves. No prompt, no error -- the exact key substitution the
  // TOFU prompt exists to catch.
  it('prompts on a key change for a worker whose pin was written during a concurrent open', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const keys = new Map([['w1', 1], ['w2', 2]])
    const transport = makeKeyedTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, keys)
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as KeyPinDecision)
    const cm = makeIdentityCipherManager(transport, { keyPins: acceptingKeyPins(confirmKeyPin) })
    try {
      const p1 = cm.getOrOpenChannel('w1')
      const p2 = cm.getOrOpenChannel('w2')
      await waitForPendingChannel(cm, 'ch-1')
      await waitForPendingChannel(cm, 'ch-2')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])
      expect(confirmKeyPin).not.toHaveBeenCalled()

      // w1 now presents a different key.
      keys.set('w1', 9)
      const p3 = cm.openChannel('w1')
      await waitForPendingChannel(cm, 'ch-3')
      simulatePingAcceptOnWs(ws!, 'ch-3')
      await p3

      expect(confirmKeyPin).toHaveBeenCalledOnce()
      expect(confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String))
      // The accepted key replaced the old pin, and w2's is still there.
      expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).sort()).toEqual(['w1', 'w2'])
    }
    finally {
      cm.closeAll()
    }
  })

  // The pin is only worth recording once a channel to that key has proven itself: every
  // exit before the ping rolls the open back, so a key that never worked must not be
  // the one a later open silently trusts.
  it('does not pin a key whose open failed verification', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('w1')
      await waitForPendingChannel(cm, 'ch-1')
      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await expect(open).rejects.toThrow('session is dead')

      expect(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).toEqual({})
    }
    finally {
      cm.closeAll()
    }
  })
})
