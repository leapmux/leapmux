import type { ChannelSocket } from './channelRelay'
import type { ChannelMessage } from '~/generated/leapmux/v1/channel_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChannelMessageSchema, HubControlFrameSchema } from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { ChannelRelay, HUB_CONTROL_CHANNEL_ID } from './channelRelay'

function frame(msg: ChannelMessage): ArrayBuffer {
  const data = toBinary(ChannelMessageSchema, msg)
  const buf = new Uint8Array(4 + data.length)
  new DataView(buf.buffer).setUint32(0, data.length)
  buf.set(data, 4)
  return buf.buffer
}

class FakeSocket implements ChannelSocket {
  readyState = 0 // CONNECTING
  sent: Uint8Array[] = []
  private listeners = new Map<string, Set<(ev: any) => void>>()

  send(data: Uint8Array<ArrayBuffer> | ArrayBuffer): void {
    this.sent.push(data instanceof Uint8Array ? data : new Uint8Array(data))
  }

  close(): void {
    this.readyState = 3 // CLOSED
    this.emit('close', {})
  }

  addEventListener(type: string, listener: (ev: any) => void): void {
    let set = this.listeners.get(type)
    if (!set) {
      set = new Set()
      this.listeners.set(type, set)
    }
    set.add(listener)
  }

  removeEventListener(type: string, listener: (ev: any) => void): void {
    this.listeners.get(type)?.delete(listener)
  }

  emit(type: string, ev: any): void {
    for (const cb of [...(this.listeners.get(type) ?? [])])
      cb(ev)
  }

  open(): void {
    this.readyState = 1 // OPEN
    this.emit('open', {})
  }
}

describe('channelRelay', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('ensureWebSocket opens, dedups concurrent dials, and send writes', async () => {
    const sock = new FakeSocket()
    const onFrame = vi.fn()
    const onHubControl = vi.fn()
    const onCloseDrain = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl,
      onCloseDrain,
    })

    const a = relay.ensureWebSocket()
    const b = relay.ensureWebSocket()
    expect(a).toBe(b)
    sock.open()
    await a
    expect(relay.isOpen()).toBe(true)

    relay.send(new Uint8Array([1, 2, 3]))
    expect(sock.sent).toHaveLength(1)
  })

  it('send throws when websocket is not open', () => {
    const relay = new ChannelRelay({
      createWebSocket: () => new FakeSocket(),
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    expect(() => relay.send(new Uint8Array([1]))).toThrow(ChannelError)
  })

  it('ensureWebSocket times out', async () => {
    vi.useFakeTimers()
    const sock = new FakeSocket()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 100,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const p = relay.ensureWebSocket()
    vi.advanceTimersByTime(100)
    await expect(p).rejects.toThrow(/timed out/)
  })

  it('routes length-prefixed frames to onFrame and hub control', async () => {
    const sock = new FakeSocket()
    const onFrame = vi.fn()
    const onHubControl = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl,
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    sock.open()
    await open

    const workerMsg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-1',
      ciphertext: new Uint8Array([9]),
      correlationId: 1n,
    })
    sock.emit('message', { data: frame(workerMsg) })
    expect(onFrame).toHaveBeenCalledOnce()
    expect(onFrame.mock.calls[0]![0]).toBe('ch-1')

    const hubMsg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: HUB_CONTROL_CHANNEL_ID,
      ciphertext: toBinary(HubControlFrameSchema, create(HubControlFrameSchema, {})),
      correlationId: 0n,
    })
    sock.emit('message', { data: frame(hubMsg) })
    expect(onHubControl).toHaveBeenCalledOnce()
  })

  it('drops short and length-mismatched frames', async () => {
    const sock = new FakeSocket()
    const onFrame = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    sock.open()
    await open

    sock.emit('message', { data: new Uint8Array([1, 2]).buffer })
    const bad = new Uint8Array(8)
    new DataView(bad.buffer).setUint32(0, 99)
    sock.emit('message', { data: bad.buffer })
    expect(onFrame).not.toHaveBeenCalled()
  })

  it('close drains via onCloseDrain and ignores stale socket close', async () => {
    const sock1 = new FakeSocket()
    const onCloseDrain = vi.fn()
    let createCount = 0
    const sockets = [sock1]
    const relay = new ChannelRelay({
      createWebSocket: () => {
        const s = sockets[createCount++] ?? new FakeSocket()
        return s
      },
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain,
    })
    const open1 = relay.ensureWebSocket()
    sock1.open()
    await open1

    // Current socket close drains.
    sock1.close()
    // Second arg is the terminal-close info: undefined for an ordinary close.
    expect(onCloseDrain).toHaveBeenCalledWith(false, undefined)

    // Re-open with a successor, then fire a stale close from sock1 — must not drain again.
    onCloseDrain.mockClear()
    const sock2 = new FakeSocket()
    sockets.push(sock2)
    const open2 = relay.ensureWebSocket()
    sock2.open()
    await open2
    sock1.emit('close', {})
    expect(onCloseDrain).not.toHaveBeenCalled()
  })

  it('preserves a successor dial when the prior socket closes while still dialing', async () => {
    const sock1 = new FakeSocket()
    const sock2 = new FakeSocket()
    let createCount = 0
    const sockets = [sock1, sock2]
    const onCloseDrain = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sockets[createCount++]!,
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain,
    })
    const open1 = relay.ensureWebSocket()
    sock1.open()
    await open1

    // Prior leaves OPEN but its close event is still queued; successor dial owns wsPromise.
    sock1.readyState = 2 // CLOSING
    const open2 = relay.ensureWebSocket()
    expect(createCount).toBe(2)
    expect(sock2.readyState).toBe(0) // CONNECTING

    sock1.emit('close', {})
    expect(onCloseDrain).toHaveBeenCalledWith(true, undefined)

    // Successor dial must still complete; a further ensure dedups onto it.
    const open3 = relay.ensureWebSocket()
    expect(open3).toBe(open2)
    sock2.open()
    await open2
    expect(relay.isOpen()).toBe(true)
    expect(createCount).toBe(2)
  })

  it('ignores messages from a superseded socket', async () => {
    const sock1 = new FakeSocket()
    const sock2 = new FakeSocket()
    let createCount = 0
    const sockets = [sock1, sock2]
    const onFrame = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sockets[createCount++]!,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open1 = relay.ensureWebSocket()
    sock1.open()
    await open1

    sock1.readyState = 2 // CLOSING
    const open2 = relay.ensureWebSocket()
    sock2.open()
    await open2

    const workerMsg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-1',
      ciphertext: new Uint8Array([9]),
      correlationId: 1n,
    })
    onFrame.mockClear()
    sock1.emit('message', { data: frame(workerMsg) })
    expect(onFrame).not.toHaveBeenCalled()

    sock2.emit('message', { data: frame(workerMsg) })
    expect(onFrame).toHaveBeenCalledOnce()
  })

  it('ensureWebSocket rejects on socket error', async () => {
    const sock = new FakeSocket()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const p = relay.ensureWebSocket()
    sock.emit('error', new ErrorEvent('error', { message: 'dial failed' }))
    await expect(p).rejects.toMatchObject({ source: 'transport', message: 'dial failed' })
    expect(relay.isOpen()).toBe(false)
  })

  it('accepts ArrayBufferView message data', async () => {
    const sock = new FakeSocket()
    const onFrame = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    sock.open()
    await open

    const workerMsg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-view',
      ciphertext: new Uint8Array([1]),
      correlationId: 2n,
    })
    // Node/WS path: Buffer-like view rather than a bare ArrayBuffer.
    sock.emit('message', { data: new Uint8Array(frame(workerMsg)) })
    expect(onFrame).toHaveBeenCalledOnce()
    expect(onFrame.mock.calls[0]![0]).toBe('ch-view')
  })

  it('closeWebSocket aborts an in-flight CONNECTING dial so onOpen cannot reinstall', async () => {
    const sock = new FakeSocket()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    relay.closeWebSocket()
    sock.open()
    await expect(open).rejects.toBeInstanceOf(ChannelError)
    expect(relay.isOpen()).toBe(false)
  })

  it('accepts a full-cover Uint8Array without slicing its ArrayBuffer', async () => {
    const sock = new FakeSocket()
    const onFrame = vi.fn()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame,
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    sock.open()
    await open

    const workerMsg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: 'ch-view',
      ciphertext: new Uint8Array([1]),
      correlationId: 2n,
    })
    const framed = new Uint8Array(frame(workerMsg))
    const sliceSpy = vi.spyOn(ArrayBuffer.prototype, 'slice')
    sock.emit('message', { data: framed })
    expect(onFrame).toHaveBeenCalledOnce()
    expect(sliceSpy).not.toHaveBeenCalled()
    sliceSpy.mockRestore()
  })

  it('closeWebSocket nulls the socket', async () => {
    const sock = new FakeSocket()
    const relay = new ChannelRelay({
      createWebSocket: () => sock,
      wsOpenTimeoutMs: 5_000,
      onFrame: () => {},
      onHubControl: () => {},
      onCloseDrain: () => {},
    })
    const open = relay.ensureWebSocket()
    sock.open()
    await open
    relay.closeWebSocket()
    expect(relay.isOpen()).toBe(false)
    expect(() => relay.send(new Uint8Array([1]))).toThrow(ChannelError)
  })

  // The hub refuses BOTH long-lived sockets through the same code path, but
  // only /ws/userevents had a client that read the reason. A channel socket
  // refused at the per-user cap used to surface as "channel disconnected" while
  // two unbounded loops above kept redialling into the same refusal.
  describe('terminal close', () => {
    function latchedRelay() {
      const sockets: FakeSocket[] = []
      const onCloseDrain = vi.fn()
      const onFatalClose = vi.fn()
      const relay = new ChannelRelay({
        createWebSocket: () => {
          const s = new FakeSocket()
          sockets.push(s)
          return s
        },
        wsOpenTimeoutMs: 5_000,
        onFrame: () => {},
        onHubControl: () => {},
        onCloseDrain,
        onFatalClose,
      })
      return { relay, sockets, onCloseDrain, onFatalClose }
    }

    it('latches on 1008, reports the reason, and refuses to redial', async () => {
      const { relay, sockets, onCloseDrain, onFatalClose } = latchedRelay()
      const open = relay.ensureWebSocket()
      sockets[0]!.open()
      await open

      sockets[0]!.readyState = 3
      sockets[0]!.emit('close', { code: 1008, reason: 'too_many_connections' })

      expect(onFatalClose).toHaveBeenCalledWith({ code: 1008, reason: 'too_many_connections' })
      // The drain gets the reason too, because that error is what reaches the
      // user through every "Failed to open ..." toast above.
      expect(onCloseDrain).toHaveBeenCalledWith(false, { code: 1008, reason: 'too_many_connections' })

      // Redialing cannot change a refusal, and the loops above retry on any
      // error -- so a second dial must not even be attempted.
      await expect(relay.ensureWebSocket()).rejects.toThrow(/too many places/i)
      expect(sockets).toHaveLength(1)
    })

    it('reports a fatal close once, however many sockets it closes', async () => {
      const { relay, sockets, onFatalClose } = latchedRelay()
      const open = relay.ensureWebSocket()
      sockets[0]!.open()
      await open

      sockets[0]!.readyState = 3
      sockets[0]!.emit('close', { code: 1008, reason: 'too_many_connections' })
      sockets[0]!.emit('close', { code: 1008, reason: 'too_many_connections' })

      expect(onFatalClose).toHaveBeenCalledTimes(1)
    })

    it('leaves an ordinary close recoverable', async () => {
      const { relay, sockets, onCloseDrain, onFatalClose } = latchedRelay()
      const open = relay.ensureWebSocket()
      sockets[0]!.open()
      await open

      // 1006 is an abnormal transport drop -- a network blip, not a refusal.
      sockets[0]!.readyState = 3
      sockets[0]!.emit('close', { code: 1006, reason: '' })

      expect(onFatalClose).not.toHaveBeenCalled()
      expect(onCloseDrain).toHaveBeenCalledWith(false, undefined)

      const redial = relay.ensureWebSocket()
      sockets[1]!.open()
      await expect(redial).resolves.toBeUndefined()
    })

    it('clears the latch on an explicit teardown, so a new sign-in can dial', async () => {
      const { relay, sockets } = latchedRelay()
      const open = relay.ensureWebSocket()
      sockets[0]!.open()
      await open
      sockets[0]!.readyState = 3
      sockets[0]!.emit('close', { code: 1008, reason: 'too_many_connections' })
      await expect(relay.ensureWebSocket()).rejects.toThrow(ChannelError)

      relay.closeWebSocket()

      const redial = relay.ensureWebSocket()
      sockets[sockets.length - 1]!.open()
      await expect(redial).resolves.toBeUndefined()
    })
  })
})
