/**
 * Node.js E2EE channel transport for e2e tests.
 *
 * Provides a FetchChannelTransport that uses raw fetch() + JSON for RPC,
 * allowing the shared ChannelManager to work in Node.js/Bun test environments.
 * Authentication uses session cookies (Cookie header) instead of Bearer tokens.
 */

import type { ChannelTransport, KeyPinDecision, WorkerKeyBundle } from '../../../src/lib/channel'
import { Buffer } from 'node:buffer'
import { EncryptionMode } from '../../../src/generated/leapmux/v1/channel_pb'
import { ChannelManager } from '../../../src/lib/channel'

// ---- Base64 helpers for JSON ↔ bytes conversion ----

function bytesToBase64(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64')
}

function base64ToBytes(b64: string): Uint8Array {
  return new Uint8Array(Buffer.from(b64, 'base64'))
}

// ---- Fetch-based channel transport for Node.js ----

const HTTP_TO_WS_RE = /^http/

class FetchChannelTransport implements ChannelTransport {
  private userId: string

  constructor(private hubUrl: string, private cookie: string, userId: string) {
    this.userId = userId
  }

  private authedHeaders(): Record<string, string> {
    return {
      'Content-Type': 'application/json',
      'Cookie': this.cookie,
    }
  }

  async getWorkerPublicKey(workerId: string): Promise<WorkerKeyBundle> {
    const resp = await fetch(`${this.hubUrl}/leapmux.v1.ChannelService/GetWorkerPublicKey`, {
      method: 'POST',
      headers: this.authedHeaders(),
      body: JSON.stringify({ workerId }),
    })
    if (!resp.ok) {
      const body = await resp.text().catch(() => '')
      throw new Error(`GetWorkerPublicKey failed: ${resp.status} ${body}`)
    }
    const data = await resp.json() as { publicKey: string, mlkemPublicKey: string, slhdsaPublicKey: string }
    return {
      x25519PublicKey: base64ToBytes(data.publicKey),
      mlkemPublicKey: base64ToBytes(data.mlkemPublicKey),
      slhdsaPublicKey: base64ToBytes(data.slhdsaPublicKey),
    }
  }

  async getWorkerEncryptionMode(workerId: string): Promise<EncryptionMode> {
    const resp = await fetch(`${this.hubUrl}/leapmux.v1.ChannelService/GetWorkerEncryptionMode`, {
      method: 'POST',
      headers: this.authedHeaders(),
      body: JSON.stringify({ workerId }),
    })
    if (!resp.ok) {
      const body = await resp.text().catch(() => '')
      throw new Error(`GetWorkerEncryptionMode failed: ${resp.status} ${body}`)
    }
    const data = await resp.json() as { encryptionMode: string }
    // Map string enum to numeric value.
    if (data.encryptionMode === 'ENCRYPTION_MODE_CLASSIC')
      return EncryptionMode.CLASSIC
    // UNSPECIFIED and POST_QUANTUM both use hybrid PQ.
    return EncryptionMode.POST_QUANTUM
  }

  async openChannel(workerId: string, handshakePayload: Uint8Array): Promise<{ channelId: string, handshakePayload: Uint8Array }> {
    const resp = await fetch(`${this.hubUrl}/leapmux.v1.ChannelService/OpenChannel`, {
      method: 'POST',
      headers: this.authedHeaders(),
      body: JSON.stringify({
        workerId,
        handshakePayload: bytesToBase64(handshakePayload),
      }),
    })
    if (!resp.ok) {
      const body = await resp.text().catch(() => '')
      throw new Error(`OpenChannel failed: ${resp.status} ${body}`)
    }
    const data = await resp.json() as { channelId: string, handshakePayload: string }
    return { channelId: data.channelId, handshakePayload: base64ToBytes(data.handshakePayload) }
  }

  async closeChannel(channelId: string): Promise<void> {
    await fetch(`${this.hubUrl}/leapmux.v1.ChannelService/CloseChannel`, {
      method: 'POST',
      headers: this.authedHeaders(),
      body: JSON.stringify({ channelId }),
    })
  }

  createWebSocket(): WebSocket {
    const wsUrl = `${this.hubUrl.replace(HTTP_TO_WS_RE, 'ws')}/ws/channel`
    // Session cookie is sent automatically by the WebSocket implementation
    // via the Cookie header. We pass it as a custom header for Node.js WebSocket.
    const ws = new WebSocket(wsUrl, ['channel-relay'])
    // @ts-expect-error -- Node.js WebSocket supports binaryType
    ws.binaryType = 'arraybuffer'
    return ws
  }

  async confirmKeyPin(_workerId: string, _expectedFingerprint: string, _actualFingerprint: string): Promise<KeyPinDecision> {
    // Auto-accept key changes in e2e tests (fresh server instances each time).
    return 'accept'
  }

  getUserId(): string {
    return this.userId
  }
}

/** Fetch the current user's ID from the hub. */
async function fetchUserId(hubUrl: string, cookie: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetCurrentUser`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Cookie': cookie,
    },
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`fetchUserId failed: ${res.status}`)
  }
  const data = await res.json() as { user: { id: string } }
  return data.user.id
}

/** Create a ChannelManager with a fetch-based transport for e2e tests. */
export async function createTestChannelManager(hubUrl: string, cookie: string): Promise<ChannelManager> {
  const userId = await fetchUserId(hubUrl, cookie)
  // Use a longer RPC timeout for e2e tests since OpenAgent spawns a subprocess
  // that can take up to 30s to start, and the E2EE round-trip adds overhead.
  return new ChannelManager(new FetchChannelTransport(hubUrl, cookie, userId), { rpcTimeout: 60_000 })
}
