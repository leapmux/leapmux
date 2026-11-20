import type { ChannelSocket, ChannelTransport, WorkerKeyBundle } from './channel'
import type { KeyPinDecision } from './keyPinStore'
import type { HandshakeState, Session } from './noise'
import type { HybridHandshakeState } from './noise-hybrid'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { IDBObjectStore } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_MAX_MESSAGE_SIZE,
  INNER_ENVELOPE_HEADROOM,
  MAX_CHUNK_SIZE,
  MAX_CONFIGURABLE_MESSAGE_SIZE,
} from '~/generated/contracts/wire'
import {
  ChannelMessageFlags,
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcResponseSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { WorkerWatchService } from '~/generated/proto/leapmux/v1/watch_pb'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { deferred, settle } from '~/test-support/async'
import { blankClassicHandshakeState, blankHybridHandshakeState, createTestSession } from '~/test-support/channelSessionDoubles'
import { seedKeyPin, testKeyPinHex, testWorkerKeyBundle } from '~/test-support/keyPins'
import { useTestStorage } from '~/test-support/persistentStorage'
import * as browserStorage from './browserStorage'
import { accountStorageKey, KEY_KEY_PINS, readKeyPinState, setStorageAccountForTests, storageAccountId } from './browserStorage'
import { enqueueKvPut, readKvRow } from './browserStorageDb'
import { ChannelError } from './channel'
import {
  acceptingKeyPins,
  AutoOpenMockWebSocket,
  channelInternals,
  ChannelManagerTestHarness,
  clearAllKeyPins,
  createMockTransport,
  decodeWireMessage,
  encodeWireMessage,
  makeCountingTransport,
  makeIdentityCipherManager,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
  newTestChannelManager,
  sessions,
  sessionStateOf,
  settleChannelSessions,
  simulatePingAcceptOnWs,
  simulatePingErrorOnWs,
  waitForPendingChannel,
} from './channel.test-support'
import { frameAssociatedData } from './channelFraming'
import { keyFingerprintHex } from './fingerprint'
import { KeyPinRejectedError, KeyPinStore } from './keyPinStore'
import { maxReassembledMessageSize } from './reassembler'
import { procedureOf } from './rpcProcedure'

// The pin decisions these opens make commit through the pin gateway, which
// needs somewhere to persist them.
useTestStorage()

describe('ChannelManager openChannel', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should open a channel and return the channel ID', async () => {
    const channelId = await h.openTestChannel('w1')
    expect(channelId).toBeTruthy()
    expect(h.mgr.isOpen(channelId)).toBe(true)
    expect(channelInternals(h.mgr, channelId).workerId).toBe('w1')
  })

  it('reports an early open failure while the fixture waits for a socket', async () => {
    const lookupFailure = new Error('handshake lookup failed')
    const socketWait = deferred<void>()
    const waitForSocket = vi.spyOn(h.mockWs, 'waitForOpenListener').mockReturnValue(socketWait.promise)
    vi.spyOn(h.transport, 'getWorkerHandshakeParams').mockRejectedValueOnce(lookupFailure)
    const outcome = h.openTestChannel('w1').then(() => 'opened', (err: unknown) => err)

    try {
      const resultBeforeSocket = await Promise.race([
        outcome,
        settle().then(() => 'still waiting'),
      ])
      expect(waitForSocket).toHaveBeenCalledOnce()
      expect(resultBeforeSocket).toMatchObject({ message: lookupFailure.message })
    }
    finally {
      socketWait.reject(new Error('fixture socket was never opened'))
      await outcome
    }
  })

  it('reports the open failure while the fixture waits for Ping', async () => {
    const pingWaitStarted = deferred<void>()
    const pingWait = deferred<void>()
    vi.spyOn(h, 'simulatePingAccept').mockImplementationOnce(() => {
      pingWaitStarted.resolve()
      return pingWait.promise
    })
    const outcome = h.openTestChannel('w1').then(() => 'opened', (err: unknown) => err)
    try {
      await pingWaitStarted.promise
      h.mockWs.simulateClose()
      await expect(outcome).resolves.toMatchObject({ message: 'channel disconnected' })
    }
    finally {
      pingWait.resolve()
    }
  })

  it('refuses a corrupt stored pin before prompting, handshaking, or opening a Hub channel', async () => {
    const accountId = storageAccountId()!
    const workerId = 'w-corrupt'
    const row = {
      k: accountStorageKey(accountId, KEY_KEY_PINS),
      v: {
        [workerId]: { publicKeyHex: 'aa', firstSeen: 1 },
      },
      e: Date.now() + 60_000,
    }
    await enqueueKvPut(row, { publish: false }).durable
    const confirm = vi.fn().mockRejectedValue(new Error('must not prompt'))
    const handshake1 = vi.fn(mockHandshake1)
    const open = vi.spyOn(h.transport, 'openChannel')
    const manager = newTestChannelManager(h.transport, {
      keyPins: acceptingKeyPins(confirm),
      session: { handshake1, handshake2: mockHandshake2 },
    })

    try {
      await expect(manager.openChannel(workerId)).rejects.toThrow(/stored key pin map is corrupt/i)
      expect(confirm).not.toHaveBeenCalled()
      expect(handshake1).not.toHaveBeenCalled()
      expect(open).not.toHaveBeenCalled()
      expect(await readKvRow(row.k)).toEqual(row)
    }
    finally {
      manager.closeAll()
    }
  })

  // The open proves the session with WorkerSystemService.Ping. The worker
  // registers that handler under this exact procedure, so a different string
  // fails every open with a timeout.
  it('proves the session with the ping procedure the worker registers', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.settle()
    await h.mockWs.waitForOpenListener()
    h.mockWs.simulateOpen()
    await h.settle()

    const sentMsg = decodeWireMessage(h.mockWs.sent.at(-1)!)
    const envelope = fromBinary(InnerMessageSchema, sessions.get(sentMsg.channelId)!.responder.receive.decrypt(frameAssociatedData(sentMsg.channelId, sentMsg.correlationId, sentMsg.flags), sentMsg.ciphertext))
    if (envelope.kind.case !== 'request')
      throw new Error('the open must send the ping as an inner request')
    expect(envelope.kind.value.method).toBe('/leapmux.v1.WorkerSystemService/Ping')
    expect(envelope.kind.value.payload).toHaveLength(0)

    await h.sendResponseFromWorker(sentMsg.channelId, Number(sentMsg.correlationId), new Uint8Array())
    await expect(openPromise).resolves.toBe(sentMsg.channelId)
  })

  it('wraps unexpected key-pin errors as ChannelError', async () => {
    const pins = new KeyPinStore({
      confirmKeyPin: async () => {
        throw new Error('dialog boom')
      },
    })
    // Seed a pin so resolve takes the mismatch path and hits confirmKeyPin.
    await (await acceptingKeyPins().resolve('w-pin-err', testWorkerKeyBundle(1))).commit()
    const boomMgr = newTestChannelManager(createMockTransport(new MockWebSocket()), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
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
    await (await acceptingKeyPins().resolve('w-pin-rej', testWorkerKeyBundle(1))).commit()
    const rejectMgr = newTestChannelManager(createMockTransport(new MockWebSocket()), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
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

  it.each(['identity', 'pool generation'] as const)('rolls back an encrypted Ping held across retirement of the %s', async (retire) => {
    const owner = 'hub-authenticated-user'
    const successor = 'successor-user'
    const workerId = 'w-held-ping'
    const lifetime = new AbortController()
    let identity = { userId: owner, generation: 1, signal: lifetime.signal }
    setStorageAccountForTests(owner)
    await seedKeyPin('unrelated-worker', {
      publicKeyHex: testKeyPinHex('11'),
      firstSeen: 1,
    })
    const before = await readKeyPinState()
    const send = h.mockWs.send.bind(h.mockWs)
    const ping = deferred<ReturnType<typeof decodeWireMessage>>()
    vi.spyOn(h.mockWs, 'send').mockImplementation((bytes) => {
      send(bytes)
      ping.resolve(decodeWireMessage(h.mockWs.sent.at(-1)!))
    })
    const close = vi.spyOn(h.transport, 'closeChannel')
    const invalidation = vi.fn()
    const off = browserStorage.onKeyPinsInvalidated(invalidation)
    const manager = newTestChannelManager(h.transport, {
      workerTrustScope: () => identity,
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2, idleRekeyIntervalMs: 0 },
    })

    try {
      const open = manager.openChannel(workerId)
      const pooled = manager.getOrOpenChannel(workerId)
      const outcomes = Promise.allSettled([open, pooled])
      await h.mockWs.waitForOpenListener()
      h.mockWs.simulateOpen()

      const frame = await ping.promise
      const associatedData = frameAssociatedData(frame.channelId, frame.correlationId, frame.flags)
      const responder = sessions.get(frame.channelId)!.responder
      const request = fromBinary(InnerMessageSchema, responder.receive.decrypt(associatedData, frame.ciphertext))
      expect(request.kind).toMatchObject({
        case: 'request',
        value: {
          method: procedureOf(WorkerSystemService.method.ping),
          payload: new Uint8Array(),
        },
      })
      expect(manager.hasOpenChannel(workerId)).toBe(false)
      expect(await readKeyPinState()).toEqual(before)

      if (retire === 'identity') {
        lifetime.abort()
        setStorageAccountForTests(successor)
        identity = { userId: successor, generation: 2, signal: new AbortController().signal }
      }
      else {
        manager.closeAll()
        expect(identity.signal.aborted).toBe(false)
      }
      await h.sendResponseFromWorker(frame.channelId, Number(frame.correlationId), new Uint8Array())

      for (const outcome of await outcomes) {
        expect(outcome).toMatchObject({
          status: 'rejected',
          reason: expect.any(ChannelError),
        })
      }
      expect(manager.isOpen(frame.channelId)).toBe(false)
      expect(manager.hasOpenChannel(workerId)).toBe(false)
      expect(close).toHaveBeenCalledExactlyOnceWith(frame.channelId)
      expect(invalidation).not.toHaveBeenCalled()
      expect(await readKeyPinState({ accountId: owner })).toEqual(before)
      expect(await readKeyPinState({ accountId: successor })).toEqual({ pins: {}, removalRevision: 0 })
    }
    finally {
      off()
      manager.closeAll()
    }
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
    const brokenMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      rpcTimeoutFn: () => 20,
    })
    try {
      const pingFailure = expect(brokenMgr.openChannel('w1')).rejects.toThrow(
        `RPC call '${procedureOf(WorkerSystemService.method.ping)}' timed out`,
      )
      await h.settle()
      await brokenWs.waitForOpenListener()
      brokenWs.simulateOpen()
      await h.settle()
      // The worker never answers the ping.
      await pingFailure
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
    const omittedMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
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
    const omittedMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2 },
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
    const badMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
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
    const badMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
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
    const zeroMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
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
    // The session worker reassembles inbound messages up to the same ceiling.
    const session = sessionStateOf(channelId)
    expect(session.maxReassembledMessageSize).toBe(maxReassembledMessageSize(negotiated))
    expect(session.reassembly.size()).toBe(0)
  })

  it('adopts a non-default negotiated maxMessageSize and derives reassembled headroom', async () => {
    const negotiated = 1 << 20
    const customWs = new MockWebSocket()
    const customMgr = newTestChannelManager(createMockTransport(customWs, { maxMessageSize: negotiated }), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
    try {
      const openPromise = customMgr.openChannel('w1')
      await h.settle()
      await customWs.waitForOpenListener()
      customWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(customWs)
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
    const floorMgr = newTestChannelManager(createMockTransport(floorWs, { maxMessageSize: MAX_CHUNK_SIZE }), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
    try {
      const openPromise = floorMgr.openChannel('w1')
      await h.settle()
      await floorWs.waitForOpenListener()
      floorWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(floorWs)
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
    const omitMgr = newTestChannelManager(createMockTransport(omitWs, { maxMessageSize: false }), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      testPayloadBudget: payload,
    })
    try {
      const openPromise = omitMgr.openChannel('w1')
      await h.settle()
      await omitWs.waitForOpenListener()
      omitWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(omitWs)
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
    const ceilMgr = newTestChannelManager(createMockTransport(ceilWs, { maxMessageSize: false }), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      testReassembledCeiling: ceiling,
    })
    try {
      const openPromise = ceilMgr.openChannel('w1')
      await h.settle()
      await ceilWs.waitForOpenListener()
      ceilWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(ceilWs)
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
  // (backend/tunnel/channel.go's HandshakeState.Finish).
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
    const failingMgr = newTestChannelManager(transport, {
      session: {
        handshake1: mockHandshake1,
        handshake2: () => {
          throw new Error('handshake message 2 failed to verify')
        },
      },
    })
    try {
      const failure = await failingMgr.openChannel('w1').catch((err: unknown) => err)
      expect(failure).toBeInstanceOf(ChannelError)
      expect(failure).toMatchObject({ source: 'transport', message: 'handshake message 2 failed to verify' })
      expect(closed).toHaveLength(1)
      expect(failingMgr.hasOpenChannel('w1')).toBe(false)
    }
    finally {
      failingMgr.closeAll()
    }
  })

  // An open that fails before handshake 2 must still zero the handshake state in
  // the session worker: the ephemeral key and the chaining key would otherwise
  // stay in its heap for the life of the page.
  it('abandons the handshake in the session worker when the Hub refuses the open', async () => {
    const base = createMockTransport(new MockWebSocket())
    const transport: ChannelTransport = {
      ...base,
      async openChannel() {
        throw new ChannelError('rpc', 'the worker is offline')
      },
    }
    let state: HybridHandshakeState | undefined
    const handshake1 = vi.fn((_workerId: string, _keys: unknown) => {
      state = blankHybridHandshakeState()
      state.e.privateKey.fill(1)
      state.ss.ck.fill(2)
      state.ss.k.fill(3)
      state.mlkemEphemeralSecretKey.fill(4)
      return {
        handshakeState: state,
        message1: new Uint8Array(3216),
      }
    })
    const handshake2 = vi.fn(mockHandshake2)
    const refusedMgr = newTestChannelManager(transport, {
      session: { handshake1, handshake2, idleRekeyIntervalMs: 0 },
      installWakeListener: false,
      keyPins: acceptingKeyPins(),
    })
    try {
      await expect(refusedMgr.openChannel('w1')).rejects.toThrow('the worker is offline')
      await settleChannelSessions()

      expect(handshake1).toHaveBeenCalledOnce()
      expect(handshake2).not.toHaveBeenCalled()
      for (const secret of [
        state!.e.privateKey,
        state!.ss.ck,
        state!.ss.k,
        state!.mlkemEphemeralSecretKey,
      ])
        expect(secret.every(byte => byte === 0)).toBe(true)
    }
    finally {
      refusedMgr.closeAll()
    }
  })

  // A failed pin write after Ping must evict the opening record and roll back
  // the Hub registration before any caller can use the session.
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
    const throwingMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      keyPins: acceptingKeyPins(),
    })
    const commit = vi.spyOn(browserStorage, 'updateKeyPinState').mockRejectedValueOnce(new Error('pin store rejected the write'))
    try {
      const openPromise = throwingMgr.openChannel('w1')
      const rejected = expect(openPromise).rejects.toThrow(/pin store rejected the write/)
      await h.settle()
      await ws.waitForOpenListener()
      ws.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(ws)
      await rejected
      // Not a ghost: the pool must not serve it, and the Hub was told to drop it.
      expect(throwingMgr.hasOpenChannel('w1')).toBe(false)
      expect(closed).toHaveLength(1)
    }
    finally {
      commit.mockRestore()
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
    const brokenMgr = newTestChannelManager(createMockTransport(brokenWs), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      rpcTimeoutFn: () => 20,
    })
    try {
      const openPromise = brokenMgr.openChannel('w1')
      await h.settle()
      await brokenWs.waitForOpenListener()
      brokenWs.simulateOpen()
      await h.settle()
      // The worker never answers the ping: the session is dead in at least one
      // direction. The open must fail rather than cache a broken channel.
      await expect(openPromise).rejects.toThrow(`RPC call '${procedureOf(WorkerSystemService.method.ping)}' timed out`)
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
    const omittedMgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
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
    const mgr = newTestChannelManager(createMockTransport(divergedWs), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
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
    const mgr = newTestChannelManager(createMockTransport(emptyWs), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      expectedUserId: () => '',
    })
    try {
      await expect(mgr.openChannel('w1')).rejects.toThrow('The authenticated user ID is empty.')
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
    const mgr = newTestChannelManager(createMockTransport(h.mockWs), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      expectedUserId: () => undefined,
    })
    try {
      const openPromise = mgr.openChannel('w1')
      await h.settle()
      await h.mockWs.waitForOpenListener()
      h.mockWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept()
      await expect(openPromise).resolves.toBeTruthy()
    }
    finally {
      mgr.closeAll()
    }
  })

  // The matching case must proceed — the check only fires on a real disagreement.
  it('opens when the Hub agrees with the expected identity', async () => {
    sessions.clear()
    const mgr = newTestChannelManager(createMockTransport(h.mockWs), {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
      expectedUserId: () => 'hub-authenticated-user',
    })
    try {
      const openPromise = mgr.openChannel('w1')
      await h.settle()
      await h.mockWs.waitForOpenListener()
      h.mockWs.simulateOpen()
      await h.settle()
      await h.simulatePingAccept()
      await expect(openPromise).resolves.toBeTruthy()
    }
    finally {
      mgr.closeAll()
    }
  })
})

describe('ChannelManager encryption modes', () => {
  const h = new ChannelManagerTestHarness()

  it('should open a channel with classic encryption (X25519 and static ML-KEM)', async () => {
    // Create a transport that returns CLASSIC mode.
    const classicWs = new MockWebSocket()
    const classicSessions = new Map<string, { initiator: Session, responder: Session }>()

    const classicTransport: ChannelTransport = {
      async getWorkerHandshakeParams(_workerId: string): Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }> {
        return {
          keys: testWorkerKeyBundle(),
          encryptionMode: EncryptionMode.CLASSIC,
        }
      },
      async openChannel(_workerId: string, _handshakePayload: Uint8Array) {
        const channelId = `ch-classic-${Math.random().toString(36).slice(2, 8)}`
        const pair = createTestSession()
        classicSessions.set(channelId, pair)
        return {
          channelId,
          handshakePayload: new Uint8Array(48),
          userId: 'hub-authenticated-user',
          maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
        }
      },
      async closeChannel(_channelId: string) {},
      createWebSocket(): ChannelSocket {
        return classicWs
      },
    }

    // Mock classic handshake functions.
    function mockClassicHS1(_workerId: string, _keys: unknown) {
      return {
        handshakeState: blankClassicHandshakeState(),
        message1: new Uint8Array(1632),
      }
    }

    function mockClassicHS2(_state: HandshakeState, _message2: Uint8Array) {
      const entries = [
        ...classicSessions.entries(),
      ]
      const lastEntry = entries.at(-1)
      if (!lastEntry)
        throw new Error('No session registered')
      return lastEntry[1].initiator
    }

    const classicMgr = newTestChannelManager(classicTransport, {
      session: {
        classicHandshake1: mockClassicHS1,
        classicHandshake2: mockClassicHS2,
      },
      keyPins: acceptingKeyPins(),
    })

    const openPromise = classicMgr.openChannel('w-classic')
    await h.settle()
    await classicWs.waitForOpenListener()
    classicWs.simulateOpen()
    await h.settle()
    await h.simulatePingAccept(classicWs, classicSessions)

    const channelId = await openPromise
    const pair = classicSessions.get(channelId)!
    expect(classicMgr.isOpen(channelId)).toBe(true)

    // Verify a call works through the encrypted channel.
    const callPromise = classicMgr.call(channelId, 'TestMethod', new Uint8Array([1, 2]))

    const resp = create(InnerRpcResponseSchema, {
      result: {
        case: 'payload',
        value: new Uint8Array([3, 4]),
      },
    })
    const respEnv = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const respPt = toBinary(InnerMessageSchema, respEnv)
    const respCt = pair.responder.send.encrypt(frameAssociatedData(channelId, 2n, ChannelMessageFlags.UNSPECIFIED), respPt)
    // The open's session-verifying Ping consumed correlation id 1.
    classicWs.simulateMessage(encodeWireMessage(channelId, respCt, { id: 2 }))

    const result = await callPromise
    expect(result).toEqual(new Uint8Array([3, 4]))

    classicMgr.closeAll()
  })
})

function makeOpenTransport(userId?: () => string) {
  const socket = deferred<AutoOpenMockWebSocket>()
  const transport = makeCountingTransport(() => {
    const ws = new AutoOpenMockWebSocket()
    socket.resolve(ws)
    return ws
  }, userId)
  return { socket, transport }
}

describe('ChannelManager open-time verification', () => {
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
      await settleChannelSessions()

      // The racer must be waiting on the SAME open, not holding the unproven channel.
      expect(racerResult).toBeNull()
      expect(transport.openChannel).toHaveBeenCalledTimes(1)

      await simulatePingAcceptOnWs(ws!, 'ch-1')

      expect(await open).toBe('ch-1')
      expect(await racer).toBe('ch-1')
      expect(cm.hasOpenChannel('worker-1')).toBe(true)
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      cm.closeAll()
    }
  })

  it('keeps a pooled caller waiting after Ping while the pin commit is pending', async () => {
    const { socket, transport } = makeOpenTransport()
    const cm = makeIdentityCipherManager(transport)
    const commitStarted = deferred<void>()
    const releaseCommit = deferred<void>()
    const update = browserStorage.updateKeyPinState
    const pinCommit = vi.spyOn(browserStorage, 'updateKeyPinState').mockImplementationOnce(async (change, options) => {
      commitStarted.resolve()
      await releaseCommit.promise
      await update(change, options)
    })
    try {
      const open = cm.openChannel('worker-1')
      const racerResult = vi.fn()
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingAcceptOnWs(await socket.promise, 'ch-1')
      await commitStarted.promise
      const racer = cm.getOrOpenChannel('worker-1').then(racerResult)
      await settleChannelSessions()
      const stateWhileCommitting = channelInternals(cm, 'ch-1').state
      const pooledWhileCommitting = cm.hasOpenChannel('worker-1')
      const resultsWhileCommitting = [...racerResult.mock.calls]
      releaseCommit.resolve()
      await Promise.all([open, racer])

      expect(stateWhileCommitting).toBe('opening')
      expect(pooledWhileCommitting).toBe(false)
      expect(resultsWhileCommitting).toEqual([])
      expect(racerResult).toHaveBeenCalledExactlyOnceWith('ch-1')
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      releaseCommit.resolve()
      pinCommit.mockRestore()
      cm.closeAll()
    }
  })

  it('does not resolve a pin or open a Hub channel after a held lookup outlives closeAll', async () => {
    const transport = makeCountingTransport(() => new AutoOpenMockWebSocket())
    const params = await transport.getWorkerHandshakeParams('worker-1')
    const lookup = deferred<typeof params>()
    transport.getWorkerHandshakeParams = vi.fn(() => lookup.promise)
    const keyPins = acceptingKeyPins()
    const resolvePin = vi.spyOn(keyPins, 'resolve')
    const cm = makeIdentityCipherManager(transport, { keyPins })
    try {
      const open = cm.openChannel('worker-1')
      const rejected = expect(open).rejects.toThrow(/superseded/)
      expect(transport.getWorkerHandshakeParams).toHaveBeenCalledExactlyOnceWith('worker-1')
      cm.closeAll()
      lookup.resolve(params)
      await rejected
      expect(resolvePin).not.toHaveBeenCalled()
      expect(transport.openChannel).not.toHaveBeenCalled()
    }
    finally {
      cm.closeAll()
    }
  })

  it.each(['removal', 'replacement'] as const)('rejects a joined pin decision after %s during Ping', async (change) => {
    const { socket, transport } = makeOpenTransport()
    const keyPins = acceptingKeyPins()
    const cm = makeIdentityCipherManager(transport, { keyPins })
    const workerId = 'worker-1'
    await seedKeyPin(workerId, {
      publicKeyHex: testKeyPinHex(change === 'removal'
        ? '00'
        : '01'),
      firstSeen: 1,
    })
    try {
      const original = cm.openChannel(workerId)
      await waitForPendingChannel(cm, 'ch-1')
      const raced = cm.getOrOpenChannel(workerId)
      const outcomes = Promise.allSettled([original, raced])
      if (change === 'removal') {
        await browserStorage.updateKeyPinState({ kind: 'remove', workerId })
      }
      else {
        await seedKeyPin(workerId, {
          publicKeyHex: testKeyPinHex('02'),
          firstSeen: 2,
        })
      }
      await simulatePingAcceptOnWs(await socket.promise, 'ch-1')

      for (const outcome of await outcomes) {
        expect(outcome).toMatchObject({
          status: 'rejected',
          reason: expect.objectContaining({
            message: expect.stringContaining('changed since the decision'),
          }),
        })
      }
      expect(cm.hasOpenChannel(workerId)).toBe(false)
      expect(transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
      expect((await readKeyPinState()).pins[workerId]?.publicKeyHex).toBe(change === 'removal'
        ? undefined
        : testKeyPinHex('02'))
    }
    finally {
      cm.closeAll()
    }
  })

  it('aborts the pin transaction when its opening record closes during IndexedDB reads', async () => {
    const { socket, transport } = makeOpenTransport()
    const cm = makeIdentityCipherManager(transport)
    const open = cm.openChannel('worker-1')
    const outcome = open.then(() => 'opened', (err: unknown) => err)
    await waitForPendingChannel(cm, 'ch-1')
    const get = IDBObjectStore.prototype.get
    const retire = vi.fn(() => void cm.closeChannel('ch-1'))
    const heldRead = vi.spyOn(IDBObjectStore.prototype, 'get').mockImplementation(function (this: IDBObjectStore, key) {
      const request = get.call(this, key)
      if (typeof key === 'string' && key.endsWith(':key-pins-removal-revision'))
        request.addEventListener('success', retire, { once: true })
      return request
    })
    try {
      await simulatePingAcceptOnWs(await socket.promise, 'ch-1')
      expect(await outcome).toBeInstanceOf(ChannelError)
      expect(retire).toHaveBeenCalledOnce()
      expect(cm.isOpen('ch-1')).toBe(false)
      expect(transport.closeChannel).toHaveBeenCalledExactlyOnceWith('ch-1')
    }
    finally {
      heldRead.mockRestore()
      cm.closeAll()
    }
    expect((await readKeyPinState()).pins).toEqual({})
  })

  it('keeps the committed pin if its opening record closes before handout', async () => {
    const { socket, transport } = makeOpenTransport()
    const cm = makeIdentityCipherManager(transport)
    const retire = vi.fn(() => void cm.closeChannel('ch-1'))
    const off = browserStorage.onKeyPinsInvalidated(retire)
    try {
      const open = cm.openChannel('worker-1')
      const outcome = open.then(() => 'opened', (err: unknown) => err)
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingAcceptOnWs(await socket.promise, 'ch-1')
      expect(await outcome).toBeInstanceOf(ChannelError)
      expect(retire).toHaveBeenCalledOnce()
      expect(cm.isOpen('ch-1')).toBe(false)
      expect((await readKeyPinState()).pins['worker-1']?.publicKeyHex).toBe(testKeyPinHex('00'))
    }
    finally {
      off()
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
      await settleChannelSessions()

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
      await simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
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
      expect(() => cm.stream('ch-1', WorkerWatchService.method.watchEvents, {})).toThrow('channel not open')

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
      await simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await Promise.all([openDone, racedDone])
      expect(String(openErr)).toContain('session is dead')
      expect(String(racedErr)).toContain('session is dead')
    }
    finally {
      cm.closeAll()
    }
  })
})

describe('ChannelManager key pinning across concurrent opens', () => {
  beforeEach(async () => {
    await clearAllKeyPins()
  })

  afterEach(async () => {
    await clearAllKeyPins()
  })

  /** A transport whose worker keys are per-worker and mutable, so a key can be rotated mid-test. */
  function makeKeyedTransport(onCreateWs: () => AutoOpenMockWebSocket, keyByWorker: Map<string, number>): ChannelTransport {
    const transport = makeCountingTransport(onCreateWs)
    transport.getWorkerHandshakeParams = vi.fn().mockImplementation(async (workerId: string) => ({
      keys: testWorkerKeyBundle(keyByWorker.get(workerId)!),
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
      await simulatePingAcceptOnWs(ws!, 'ch-1')
      await simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])

      expect(Object.keys((await readKeyPinState()).pins).sort()).toEqual(['w1', 'w2'])
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
    const keys = new Map([
      ['w1', 1],
      ['w2', 2],
    ])
    const transport = makeKeyedTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, keys)
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as KeyPinDecision)
    const cm = makeIdentityCipherManager(transport, {
      keyPins: acceptingKeyPins(confirmKeyPin),
    })
    try {
      const p1 = cm.getOrOpenChannel('w1')
      const p2 = cm.getOrOpenChannel('w2')
      await waitForPendingChannel(cm, 'ch-1')
      await waitForPendingChannel(cm, 'ch-2')
      await simulatePingAcceptOnWs(ws!, 'ch-1')
      await simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])
      expect(confirmKeyPin).not.toHaveBeenCalled()

      // w1 now presents a different key.
      keys.set('w1', 9)
      const p3 = cm.openChannel('w1')
      await waitForPendingChannel(cm, 'ch-3')
      await simulatePingAcceptOnWs(ws!, 'ch-3')
      await p3

      expect(confirmKeyPin).toHaveBeenCalledOnce()
      expect(confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String), expect.any(AbortSignal))
      // The accepted key replaced the old pin, and w2's is still there.
      expect(Object.keys((await readKeyPinState()).pins).sort()).toEqual(['w1', 'w2'])
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
      const rejected = expect(open).rejects.toThrow('session is dead')
      await waitForPendingChannel(cm, 'ch-1')
      await simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await rejected

      expect((await readKeyPinState()).pins).toEqual({})
    }
    finally {
      cm.closeAll()
    }
  })
})

describe('ChannelManager openChannelForKeyReview', () => {
  function setup() {
    const userId = 'user-1'
    setStorageAccountForTests(userId)
    let lifetime = new AbortController()
    let identity = { userId, generation: 1, signal: lifetime.signal }
    const retireIdentity = (nextUserId = userId) => {
      lifetime.abort()
      lifetime = new AbortController()
      setStorageAccountForTests(nextUserId)
      identity = { userId: nextUserId, generation: identity.generation + 1, signal: lifetime.signal }
    }
    const { socket, transport } = makeOpenTransport(() => identity.userId)
    const confirmKeyPin = vi.fn()
    const keyPins = acceptingKeyPins(confirmKeyPin)
    const cm = makeIdentityCipherManager(transport, {
      keyPins,
      workerTrustScope: () => identity,
    })
    const params = (byte: number) => ({
      keys: testWorkerKeyBundle(byte),
      encryptionMode: EncryptionMode.CLASSIC,
    })
    return {
      cm,
      transport,
      socket,
      confirmKeyPin,
      keyPins,
      identity: () => identity,
      retireIdentity,
      params,
    }
  }

  it('serializes review behind an ordinary open and approves only the freshly proven key', async () => {
    const h = setup()
    const ordinaryLookup = deferred<ReturnType<typeof h.params>>()
    const lookup = vi.mocked(h.transport.getWorkerHandshakeParams)
    lookup.mockReturnValueOnce(ordinaryLookup.promise).mockResolvedValue(h.params(3))
    h.confirmKeyPin.mockResolvedValueOnce('reject').mockResolvedValueOnce('accept')
    const oldKey = testKeyPinHex('01')
    const verified = testKeyPinHex('03')
    await seedKeyPin('worker', { publicKeyHex: oldKey, firstSeen: 1 })
    try {
      const ordinary = h.cm.openChannel('worker')
      const rejected = expect(ordinary).rejects.toThrow('Worker public key rejected by user')
      const firstReview = h.cm.openChannelForKeyReview('worker')
      const secondReview = h.cm.openChannelForKeyReview('worker')
      expect(lookup).toHaveBeenCalledExactlyOnceWith('worker')
      ordinaryLookup.resolve(h.params(2))
      await rejected

      await waitForPendingChannel(h.cm, 'ch-1')
      expect((await readKeyPinState()).pins.worker?.publicKeyHex).toBe(oldKey)
      await simulatePingAcceptOnWs(await h.socket.promise, 'ch-1')
      await expect(Promise.all([firstReview, secondReview])).resolves.toEqual([
        {
          publicKeyHex: verified,
          scope: h.identity(),
        },
        {
          publicKeyHex: verified,
          scope: h.identity(),
        },
      ])
      expect(lookup).toHaveBeenCalledTimes(2)
      expect(h.confirmKeyPin).toHaveBeenNthCalledWith(
        2,
        'worker',
        keyFingerprintHex(oldKey),
        keyFingerprintHex(verified),
        expect.any(AbortSignal),
      )
      expect((await readKeyPinState()).pins.worker?.publicKeyHex).toBe(verified)

      lookup.mockResolvedValue(h.params(4))
      await expect(h.cm.openChannel('worker')).rejects.toThrow('Worker public key rejected by user')
      expect(h.confirmKeyPin).toHaveBeenCalledTimes(2)
    }
    finally {
      h.cm.closeAll()
    }
  })

  it('preserves the explicit rejection and lets the next review prompt again', async () => {
    const h = setup()
    const previous = testKeyPinHex('01')
    const candidate = testKeyPinHex('02')
    await seedKeyPin('worker', { publicKeyHex: previous, firstSeen: 1 })
    vi.mocked(h.transport.getWorkerHandshakeParams).mockResolvedValue(h.params(2))
    h.confirmKeyPin.mockResolvedValueOnce('reject').mockResolvedValueOnce('accept')
    try {
      await expect(h.cm.openChannelForKeyReview('worker')).rejects.toBeInstanceOf(KeyPinRejectedError)
      expect(h.transport.openChannel).not.toHaveBeenCalled()
      expect((await readKeyPinState()).pins.worker?.publicKeyHex).toBe(previous)

      const next = h.cm.openChannelForKeyReview('worker')
      await waitForPendingChannel(h.cm, 'ch-1')
      await simulatePingAcceptOnWs(await h.socket.promise, 'ch-1')
      await expect(next).resolves.toEqual({
        publicKeyHex: candidate,
        scope: h.identity(),
      })
      expect(h.confirmKeyPin).toHaveBeenCalledTimes(2)
      expect((await readKeyPinState()).pins.worker?.publicKeyHex).toBe(candidate)
    }
    finally {
      h.cm.closeAll()
    }
  })

  it('requires an existing pin when reviewing a key', async () => {
    const h = setup()
    try {
      await expect(h.cm.openChannelForKeyReview('worker')).rejects.toThrow('The Worker has no stored key to review.')
      expect(h.transport.openChannel).not.toHaveBeenCalled()
      expect(h.confirmKeyPin).not.toHaveBeenCalled()
      expect((await readKeyPinState()).pins).toEqual({})
    }
    finally {
      h.cm.closeAll()
    }
  })

  it('retires held ordinary and review opens when the relay closes', async () => {
    const h = setup()
    const lookup = vi.mocked(h.transport.getWorkerHandshakeParams)
    lookup.mockResolvedValueOnce(h.params(1))
    try {
      const active = h.cm.openChannel('bootstrap')
      const ws = await h.socket.promise
      await waitForPendingChannel(h.cm, 'ch-1')
      await simulatePingAcceptOnWs(ws, 'ch-1')
      await active

      const held = deferred<ReturnType<typeof h.params>>()
      lookup.mockReturnValueOnce(held.promise)
      const resolve = vi.spyOn(h.keyPins, 'resolve')
      const ordinary = h.cm.openChannel('worker')
      const review = h.cm.openChannelForKeyReview('worker')
      const outcomes = Promise.allSettled([ordinary, review])
      ws.simulateClose()
      held.resolve(h.params(2))

      for (const outcome of await outcomes) {
        expect(outcome).toMatchObject({
          status: 'rejected',
          reason: expect.objectContaining({ message: 'channel disconnected' }),
        })
      }
      expect(lookup).toHaveBeenCalledTimes(2)
      expect(resolve).not.toHaveBeenCalled()
      expect(h.transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      h.cm.closeAll()
    }
  })

  it.each(['same-user sign-in', 'account switch', 'closeAll'] as const)('rejects held ordinary and review opens after %s', async (transition) => {
    const h = setup()
    const held = deferred<ReturnType<typeof h.params>>()
    const lookup = vi.mocked(h.transport.getWorkerHandshakeParams)
    lookup.mockReturnValueOnce(held.promise)
    const resolve = vi.spyOn(h.keyPins, 'resolve')
    try {
      const ordinary = h.cm.openChannel('worker')
      const review = h.cm.openChannelForKeyReview('worker')
      const outcome = Promise.allSettled([ordinary, review])
      expect(lookup).toHaveBeenCalledExactlyOnceWith('worker')
      if (transition === 'closeAll') {
        h.cm.closeAll()
      }
      else {
        h.retireIdentity(transition === 'account switch'
          ? 'user-2'
          : 'user-1')
      }

      held.resolve(h.params(2))
      expect((await outcome).map(result => result.status)).toEqual(['rejected', 'rejected'])
      expect(lookup).toHaveBeenCalledExactlyOnceWith('worker')
      expect(resolve).not.toHaveBeenCalled()
      expect(h.transport.openChannel).not.toHaveBeenCalled()
    }
    finally {
      h.cm.closeAll()
    }
  })
})
