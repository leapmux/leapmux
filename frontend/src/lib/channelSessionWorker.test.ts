import type { SessionCommand, SessionEvent } from './channelSessionProtocol'
import { x25519 } from '@noble/curves/ed25519.js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'

/** The message handler of the test's global scope, before the worker module replaced it. */
const scopeMessageHandler = globalThis.onmessage

/**
 * Load the worker module afresh, with the `postMessage` and the `close` of a
 * worker's global scope stubbed, and return the stubs and its message handler.
 */
async function loadWorker() {
  const posted = vi.fn()
  const close = vi.fn()
  vi.stubGlobal('postMessage', posted)
  vi.stubGlobal('close', close)
  vi.resetModules()
  await import('./channelSessionWorker')
  const onmessage = globalThis.onmessage as (event: MessageEvent<SessionCommand>) => void
  return {
    posted,
    close,
    send: (command: SessionCommand) => onmessage(new MessageEvent('message', { data: command })),
  }
}

afterEach(() => {
  globalThis.onmessage = scopeMessageHandler
  vi.unstubAllGlobals()
})

describe('channelSessionWorker', () => {
  it('answers a command of the main thread through postMessage, and transfers the buffers of the event', async () => {
    const worker = await loadWorker()

    worker.send({
      type: 'beginHandshake',
      requestId: 3,
      mode: EncryptionMode.CLASSIC,
      keys: {
        x25519PublicKey: x25519.getPublicKey(x25519.utils.randomSecretKey()),
        mlkemPublicKey: new Uint8Array(0),
        slhdsaPublicKey: new Uint8Array(0),
      },
    })

    expect(worker.posted).toHaveBeenCalledOnce()
    const [event, options] = worker.posted.mock.calls[0]! as [SessionEvent, StructuredSerializeOptions]
    expect(event).toMatchObject({ type: 'handshakeBegun', requestId: 3 })
    const { message1 } = event as Extract<SessionEvent, { type: 'handshakeBegun' }>
    expect(message1.byteLength).toBeGreaterThan(0)
    expect(options).toEqual({
      transfer: [message1.buffer],
    })
  })

  it('stops the worker on shutdown, and answers nothing after it', async () => {
    const worker = await loadWorker()

    worker.send({ type: 'shutdown' })
    worker.send({ type: 'ensureReady', requestId: 4, channelId: 'ch-1' })

    expect(worker.close).toHaveBeenCalledOnce()
    expect(worker.posted).not.toHaveBeenCalled()
  })
})
