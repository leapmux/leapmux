import type { ChannelTransport, WorkerKeyBundle } from '~/lib/channel'
import type { Session } from '~/lib/noise'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  UserIdClaimResponseSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelError, ChannelManager } from '~/lib/channel'

// ---------------------------------------------------------------------------
// Mock WebSocket that auto-opens on next microtask
// ---------------------------------------------------------------------------

class MockWebSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  readyState = MockWebSocket.CONNECTING
  binaryType = 'arraybuffer'

  constructor() {
    super()
    // Auto-open on next microtask (simulates successful TCP + upgrade).
    queueMicrotask(() => {
      if (this.readyState === MockWebSocket.CONNECTING) {
        this.readyState = MockWebSocket.OPEN
        this.dispatchEvent(new Event('open'))
      }
    })
  }

  /** Fire the 'close' event and set readyState to CLOSED. */
  simulateClose() {
    this.readyState = MockWebSocket.CLOSED
    this.dispatchEvent(new Event('close'))
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
  }

  send(_data: unknown) {
    // no-op in tests
  }
}

// ---------------------------------------------------------------------------
// Mock Noise session
// ---------------------------------------------------------------------------

function makeMockSession(): Session {
  return {
    send: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
    },
    receive: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
    },
  } as Session
}

// ---------------------------------------------------------------------------
// Mock transport
// ---------------------------------------------------------------------------

function makeMockTransport(onCreateWs: () => MockWebSocket): ChannelTransport {
  return {
    getWorkerHandshakeParams: vi.fn().mockResolvedValue({
      keys: {
        x25519PublicKey: new Uint8Array(32),
        mlkemPublicKey: new Uint8Array(0),
        slhdsaPublicKey: new Uint8Array(0),
      } satisfies WorkerKeyBundle,
      encryptionMode: EncryptionMode.CLASSIC,
    }),
    openChannel: vi.fn().mockResolvedValue({
      channelId: 'ch-1',
      handshakePayload: new Uint8Array(48),
    }),
    closeChannel: vi.fn().mockResolvedValue(undefined),
    createWebSocket: () => onCreateWs() as unknown as WebSocket,
    confirmKeyPin: vi.fn().mockResolvedValue('accept' as const),
    getUserId: () => 'user-1',
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Simulate a successful UserIdClaimResponse arriving on the WebSocket.
 * Since mock sessions use identity encrypt/decrypt, we can build the
 * wire message directly without real crypto.
 */
function simulateClaimAcceptOnWs(ws: MockWebSocket, channelId: string) {
  const claimResp = create(UserIdClaimResponseSchema, { success: true })
  const envelope = create(InnerMessageSchema, {
    kind: { case: 'userIdClaimResponse', value: claimResp },
  })
  const plaintext = toBinary(InnerMessageSchema, envelope)

  // Build a ChannelMessage with the plaintext as ciphertext (identity cipher).
  const channelMsg = create(ChannelMessageSchema, {
    channelId,
    ciphertext: plaintext,
  })
  const msgBytes = toBinary(ChannelMessageSchema, channelMsg)

  // Wire format: 4-byte big-endian length prefix + protobuf bytes.
  const frame = new Uint8Array(4 + msgBytes.length)
  new DataView(frame.buffer).setUint32(0, msgBytes.length)
  frame.set(msgBytes, 4)

  ws.dispatchEvent(new MessageEvent('message', { data: frame.buffer }))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('channelManager handleWebSocketClose', () => {
  it('should reject pending claim when websocket closes during claim exchange', async () => {
    let ws: MockWebSocket | null = null
    const transport = makeMockTransport(() => {
      ws = new MockWebSocket()
      return ws
    })

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    // Start openChannel — it will wait for the UserIdClaim response
    // after the WebSocket auto-opens.
    const openPromise = cm.openChannel('worker-1')

    // Flush microtasks so openChannel progresses through:
    //   HTTP RPCs → handshake → ensureWebSocket (auto-open) → sendUserIdClaim
    // The claim is now pending (no server response).
    await new Promise(r => setTimeout(r, 10))

    // The WebSocket should have been created and opened by now.
    expect(ws).not.toBeNull()
    expect(ws!.readyState).toBe(MockWebSocket.OPEN)

    // Simulate WebSocket close (e.g. backend restarts during claim exchange).
    ws!.simulateClose()

    // openChannel should reject with a transport error, NOT hang forever.
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel disconnected')
  })
})

describe('channelManager closeChannel during claim', () => {
  it('should reject pending claim when closeChannel is called during claim exchange', async () => {
    let ws: MockWebSocket | null = null
    const transport = makeMockTransport(() => {
      ws = new MockWebSocket()
      return ws
    })

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    const openPromise = cm.openChannel('worker-1')

    // Let openChannel progress to sendUserIdClaim (claim pending).
    await new Promise(r => setTimeout(r, 10))

    // Close the channel while the claim is still pending.
    await cm.closeChannel('ch-1')

    // openChannel should reject, NOT hang forever.
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed')
  })
})

describe('channelManager CLOSE sentinel during claim', () => {
  it('should reject pending claim when server sends CLOSE during claim exchange', async () => {
    let ws: MockWebSocket | null = null
    const transport = makeMockTransport(() => {
      ws = new MockWebSocket()
      return ws
    })

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    const openPromise = cm.openChannel('worker-1')

    // Let openChannel progress to sendUserIdClaim (claim pending).
    await new Promise(r => setTimeout(r, 10))

    // Simulate server sending a CLOSE sentinel for the channel.
    const closeMsg = create(ChannelMessageSchema, {
      channelId: 'ch-1',
      flags: ChannelMessageFlags.CLOSE,
    })
    const msgBytes = toBinary(ChannelMessageSchema, closeMsg)
    const frame = new Uint8Array(4 + msgBytes.length)
    new DataView(frame.buffer).setUint32(0, msgBytes.length)
    frame.set(msgBytes, 4)
    ws!.dispatchEvent(new MessageEvent('message', { data: frame.buffer }))

    // openChannel should reject, NOT hang forever.
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed by server')
  })
})

describe('channelManager getOrOpenChannel deduplication', () => {
  it('should return the same channel for concurrent calls to the same worker', async () => {
    let ws: MockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new MockWebSocket()
      return ws
    })
    // Each openChannel call gets a unique channel ID so we can detect duplicates.
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
    }))

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    // Launch two concurrent getOrOpenChannel calls for the same worker.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-1')

    // Flush microtasks so the handshake + WebSocket open progresses.
    await new Promise(r => setTimeout(r, 10))

    // Simulate claim acceptance for the single channel that was opened.
    // Build and dispatch a UserIdClaimResponse via the WebSocket message handler.
    simulateClaimAcceptOnWs(ws!, 'ch-1')

    const [ch1, ch2] = await Promise.all([p1, p2])

    // Both should resolve to the same channel — only one openChannel call.
    expect(ch1).toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(1)
  })

  it('should open separate channels for different workers', async () => {
    let ws: MockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new MockWebSocket()
      return ws
    })
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
    }))

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    // Launch concurrent getOrOpenChannel calls for different workers.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-2')

    await new Promise(r => setTimeout(r, 10))

    // Accept claims for both channels.
    simulateClaimAcceptOnWs(ws!, 'ch-1')
    simulateClaimAcceptOnWs(ws!, 'ch-2')

    const [ch1, ch2] = await Promise.all([p1, p2])

    expect(ch1).not.toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(2)
  })
})
