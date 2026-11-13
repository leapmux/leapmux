import type { SessionWorkerHandlers, SessionWorkerPort } from './channelSessionClient'
import type { SessionCommand, SessionEvent } from './channelSessionProtocol'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChannelMessageFlags, ChannelMessageSchema, EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { frameBytes, unframeBytes } from './channelFraming'
import { ChannelSessionClient, SESSION_REQUEST_BUDGET_MS, spawnChannelSessionWorker } from './channelSessionClient'
import { isDebugEnabled, setDebugEnabled } from './logger'

const keys = {
  x25519PublicKey: new Uint8Array(32),
  mlkemPublicKey: new Uint8Array(1568),
  slhdsaPublicKey: new Uint8Array(64),
}

/** A session worker port that records each command with its transfer list, and lets the test post events. */
class FakePort implements SessionWorkerPort {
  posted: Array<{ command: SessionCommand, transfer: Transferable[] }> = []
  handlers: SessionWorkerHandlers | undefined
  terminate = vi.fn()

  postMessage(command: SessionCommand, transfer: Transferable[]): void {
    this.posted.push({ command, transfer })
  }

  connect(handlers: SessionWorkerHandlers): void {
    this.handlers = handlers
  }

  /** Post `event` from the worker. */
  emit(event: SessionEvent): void {
    this.handlers!.message(event)
  }

  /** The commands of `type`, in post order. */
  commands<T extends SessionCommand['type']>(type: T): Array<Extract<SessionCommand, { type: T }>> {
    return this.posted.map(entry => entry.command).filter((command): command is Extract<SessionCommand, { type: T }> => command.type === type)
  }
}

function clientHarness(opts?: { spawn?: () => SessionWorkerPort, requestBudgetMs?: number }) {
  const ports: FakePort[] = []
  const spawn = vi.fn((): SessionWorkerPort => {
    const port = new FakePort()
    ports.push(port)
    return port
  })
  const deps = {
    spawn: opts?.spawn ?? spawn,
    onWire: vi.fn(),
    onDeliver: vi.fn(),
    onReassemblyFailed: vi.fn(),
    onSessionEnded: vi.fn(),
    onWorkerLost: vi.fn(),
    ...(opts?.requestBudgetMs === undefined
      ? {}
      : { requestBudgetMs: opts.requestBudgetMs }),
  }
  const client = new ChannelSessionClient(deps)
  return { client, ports, spawn, deps }
}

/** Begin a handshake, and answer it from the worker. */
async function begin(h: ReturnType<typeof clientHarness>, port = 0) {
  const pending = h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
  const command = h.ports[port]!.commands('beginHandshake').at(-1)!
  h.ports[port]!.emit({
    type: 'handshakeBegun',
    requestId: command.requestId,
    message1: new Uint8Array([1, 2]),
  })
  return pending
}

const failure = {
  source: 'transport',
  message: 'noise-hybrid: SLH-DSA signature verification failed',
  code: 0,
  fatal: false,
  disconnected: true,
} as const

afterEach(() => {
  vi.useRealTimers()
})

/** A stand-in for the browser's Worker that records how it was built and what it received. */
class FakeBrowserWorker {
  static spawned: FakeBrowserWorker[] = []
  onmessage: ((event: { data: unknown }) => void) | null = null
  onerror: ((event: { message: string }) => void) | null = null
  onmessageerror: (() => void) | null = null
  posted: Array<{ message: unknown, transfer: unknown }> = []
  terminate = vi.fn()

  constructor(readonly url: URL, readonly options: WorkerOptions) {
    FakeBrowserWorker.spawned.push(this)
  }

  postMessage(message: unknown, transfer: unknown): void {
    this.posted.push({ message, transfer })
  }
}

describe('spawnChannelSessionWorker', () => {
  afterEach(() => {
    FakeBrowserWorker.spawned = []
    vi.unstubAllGlobals()
  })

  /** Spawn through the browser path, connect it, and return the port, its Worker and the handlers. */
  function spawnInBrowser() {
    vi.stubGlobal('Worker', FakeBrowserWorker)
    const port = spawnChannelSessionWorker()
    const handlers = {
      message: vi.fn(),
      failure: vi.fn(),
    }
    port.connect(handlers)
    const worker = FakeBrowserWorker.spawned[0]
    if (worker === undefined)
      throw new Error('spawnChannelSessionWorker built no Worker')
    return { port, worker, handlers }
  }

  it('builds a module worker from the channel session worker module', () => {
    const { worker } = spawnInBrowser()

    expect(FakeBrowserWorker.spawned).toHaveLength(1)
    expect(worker.url.pathname).toMatch(/\/src\/lib\/channelSessionWorker\.ts$/)
    expect(worker.options).toEqual({ type: 'module' })
  })

  it('posts each command with its transfer list, delivers each event, and terminates the Worker', () => {
    const { port, worker, handlers } = spawnInBrowser()
    const plaintext = new Uint8Array([1])
    const command: SessionCommand = {
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext,
      startsRequest: true,
    }

    port.postMessage(command, [plaintext.buffer])
    worker.onmessage?.({
      data: { type: 'requestDone', requestId: 4 },
    })
    port.terminate()

    expect(worker.posted).toEqual([
      {
        message: command,
        transfer: [plaintext.buffer],
      },
    ])
    expect(handlers.message).toHaveBeenCalledExactlyOnceWith({ type: 'requestDone', requestId: 4 })
    expect(handlers.failure).not.toHaveBeenCalled()
    expect(worker.terminate).toHaveBeenCalledOnce()
  })

  it('reports an error of the Worker with its message, and a load failure that carries none', () => {
    const { worker, handlers } = spawnInBrowser()

    worker.onerror?.({ message: 'SyntaxError: Unexpected token' })
    worker.onerror?.({ message: '' })

    expect(handlers.failure.mock.calls).toEqual([
      ['SyntaxError: Unexpected token'],
      ['the worker failed to load'],
    ])
  })

  it('reports a message that the Worker posted and this thread cannot read', () => {
    const { worker, handlers } = spawnInBrowser()

    worker.onmessageerror?.()

    expect(handlers.failure).toHaveBeenCalledExactlyOnceWith('a message from the worker could not be read')
    expect(handlers.message).not.toHaveBeenCalled()
  })
})

describe('ChannelSessionClient spawn', () => {
  it('spawns nothing until prepare or beginHandshake, and spawns once for both', async () => {
    const h = clientHarness()
    expect(h.spawn).not.toHaveBeenCalled()

    h.client.prepare()
    expect(h.spawn).toHaveBeenCalledOnce()
    void h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys)

    expect(h.spawn).toHaveBeenCalledOnce()
    expect(h.ports[0]!.commands('beginHandshake')).toHaveLength(1)
  })

  it('posts configure first, with the current debug flag, and again on a change', () => {
    const was = isDebugEnabled()
    try {
      const h = clientHarness()
      h.client.prepare()
      expect(h.ports[0]!.posted[0]!.command).toEqual({ type: 'configure', debug: was })

      setDebugEnabled(!was)

      expect(h.ports[0]!.commands('configure')).toEqual([
        { type: 'configure', debug: was },
        { type: 'configure', debug: !was },
      ])
    }
    finally {
      setDebugEnabled(was)
    }
  })

  it('swallows a spawn failure in prepare, rejects beginHandshake with it, and tries again on the next call', async () => {
    let attempts = 0
    const port = new FakePort()
    const h = clientHarness({
      spawn: () => {
        attempts++
        if (attempts <= 2)
          throw new Error('blocked by CSP')
        return port
      },
    })

    expect(() => h.client.prepare()).not.toThrow()
    await expect(h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys))
      .rejects
      .toMatchObject({ source: 'transport', message: 'channel session worker could not start: blocked by CSP' })
    expect(attempts).toBe(2)

    const pending = h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    expect(attempts).toBe(3)
    const command = port.commands('beginHandshake')[0]!
    port.emit({
      type: 'handshakeBegun',
      requestId: command.requestId,
      message1: new Uint8Array([1]),
    })
    await expect(pending).resolves.toMatchObject({ id: command.requestId })
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
  })
})

describe('ChannelSessionClient requests', () => {
  it('resolves beginHandshake on handshakeBegun, under the id of its request', async () => {
    const h = clientHarness()

    const handshake = await begin(h)

    const command = h.ports[0]!.commands('beginHandshake')[0]!
    expect(command).toMatchObject({ type: 'beginHandshake', mode: EncryptionMode.POST_QUANTUM, keys })
    expect(h.ports[0]!.posted.find(entry => entry.command === command)!.transfer).toEqual([])
    expect(handshake).toEqual({
      id: command.requestId,
      message1: new Uint8Array([1, 2]),
    })
  })

  it('rejects with a ChannelError that carries the source, the message and the disconnected flag on requestFailed', async () => {
    const h = clientHarness()
    const pending = h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    const { requestId } = h.ports[0]!.commands('beginHandshake')[0]!

    h.ports[0]!.emit({ type: 'requestFailed', requestId, error: failure })

    const err = await pending.catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ChannelError)
    expect(err).toMatchObject({ source: 'transport', message: failure.message, disconnected: true })
  })

  it('finishes a handshake on the worker that began it, and resolves on requestDone', async () => {
    const h = clientHarness()
    const handshake = await begin(h)
    const payload = new Uint8Array([9])

    const finished = h.client.finishHandshake(handshake, { channelId: 'ch-1', payload, maxReassembledMessageSize: 100 })
    const command = h.ports[0]!.commands('finishHandshake')[0]!
    expect(command).toMatchObject({
      handshakeId: handshake.id,
      channelId: 'ch-1',
      payload,
      maxReassembledMessageSize: 100,
    })
    h.ports[0]!.emit({ type: 'requestDone', requestId: command.requestId })

    await expect(finished).resolves.toBeUndefined()
  })

  it('rejects a request whose answer has the wrong type', async () => {
    const h = clientHarness()
    const pending = h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    const { requestId } = h.ports[0]!.commands('beginHandshake')[0]!

    h.ports[0]!.emit({ type: 'requestDone', requestId })

    await expect(pending).rejects.toThrow(/answered request \d+ with requestDone, not handshakeBegun/)
  })

  it('posts abandonHandshake only to the worker that holds the handshake', async () => {
    const h = clientHarness()
    const handshake = await begin(h)

    h.client.abandonHandshake(handshake)
    h.client.abandonHandshake(handshake)

    expect(h.ports[0]!.commands('abandonHandshake')).toEqual([
      { type: 'abandonHandshake', handshakeId: handshake.id },
    ])
  })

  it('rejects ensureReady when no worker runs, and resolves it on requestDone', async () => {
    const h = clientHarness()
    await expect(h.client.ensureReady('ch-1')).rejects.toMatchObject({ message: 'channel session worker is not running' })
    expect(h.spawn).not.toHaveBeenCalled()

    h.client.prepare()
    const ready = h.client.ensureReady('ch-1')
    const command = h.ports[0]!.commands('ensureReady')[0]!
    expect(command.channelId).toBe('ch-1')
    h.ports[0]!.emit({ type: 'requestDone', requestId: command.requestId })

    await expect(ready).resolves.toBeUndefined()
  })

  it('spends a handshake in its first finish, and rejects a second finish of it without a post', async () => {
    const h = clientHarness()
    const handshake = await begin(h)
    const params = {
      channelId: 'ch-1',
      payload: new Uint8Array([9]),
      maxReassembledMessageSize: 100,
    }
    const first = h.client.finishHandshake(handshake, params)
    const { requestId } = h.ports[0]!.commands('finishHandshake')[0]!
    h.ports[0]!.emit({ type: 'requestFailed', requestId, error: failure })
    await expect(first).rejects.toMatchObject({ message: failure.message })

    await expect(h.client.finishHandshake(handshake, params))
      .rejects
      .toMatchObject({ source: 'transport', message: 'channel session worker stopped' })
    expect(h.ports[0]!.commands('finishHandshake')).toHaveLength(1)
  })

  it('stops the budget timer of a request that its answer settled', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    const h = clientHarness({ requestBudgetMs: 1_000 })
    await begin(h)

    await vi.advanceTimersByTimeAsync(1_000)

    expect(h.ports[0]!.terminate).not.toHaveBeenCalled()
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
  })

  it('warns about an answer to a request that does not wait, and settles nothing', async () => {
    const h = clientHarness()
    const handshake = await begin(h)
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    h.ports[0]!.emit({
      type: 'handshakeBegun',
      requestId: handshake.id,
      message1: new Uint8Array([3]),
    })
    h.ports[0]!.emit({ type: 'requestFailed', requestId: 999, error: failure })

    expect(warnSpy.mock.calls).toEqual([
      [
        '[channel]',
        'the channel session worker answered a request that does not wait',
        { request_id: handshake.id },
      ],
      [
        '[channel]',
        'the channel session worker answered a request that does not wait',
        { request_id: 999 },
      ],
    ])
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
    warnSpy.mockRestore()
  })

  it('gives each request a fresh id, also across workers', async () => {
    const h = clientHarness()
    await begin(h)
    h.ports[0]!.handlers!.failure('boom')
    await begin(h, 1)

    const ids = [
      ...h.ports[0]!.commands('beginHandshake'),
      ...h.ports[1]!.commands('beginHandshake'),
    ].map(command => command.requestId)
    expect(new Set(ids).size).toBe(2)
  })
})

describe('ChannelSessionClient transfers', () => {
  it('transfers a full-cover send plaintext', () => {
    const h = clientHarness()
    h.client.prepare()
    const plaintext = new Uint8Array([1, 2, 3])

    h.client.send('ch-1', 5, plaintext, true)

    const { command, transfer } = h.ports[0]!.posted.at(-1)!
    expect(command).toEqual({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 5,
      plaintext,
      startsRequest: true,
    })
    expect(transfer).toEqual([plaintext.buffer])
  })

  it('copies a subarray plaintext before the transfer, so the caller keeps its buffer', () => {
    const h = clientHarness()
    h.client.prepare()
    const whole = new Uint8Array([1, 2, 3, 4, 5])

    h.client.send('ch-1', 5, whole.subarray(1, 3), false)

    const { command, transfer } = h.ports[0]!.posted.at(-1)!
    const sent = (command as Extract<SessionCommand, { type: 'send' }>).plaintext
    expect([...sent]).toEqual([2, 3])
    expect(transfer).toEqual([sent.buffer])
    expect(transfer[0]).not.toBe(whole.buffer)
    expect(whole.byteLength).toBe(5)
  })

  it('transfers the whole frame buffer of an inbound message', () => {
    const h = clientHarness()
    h.client.prepare()
    const framed = frameBytes(toBinary(ChannelMessageSchema, create(ChannelMessageSchema, {
      channelId: 'ch-1',
      ciphertext: new Uint8Array([9, 9, 9]),
      correlationId: 4n,
      flags: ChannelMessageFlags.MORE,
    })))
    const unframed = unframeBytes(framed)
    if (!unframed.ok)
      throw new Error('the frame failed to unframe')
    const msg = fromBinary(ChannelMessageSchema, unframed.payload)

    h.client.forwardInbound('ch-1', msg)

    const { command, transfer } = h.ports[0]!.posted.at(-1)!
    expect(command).toMatchObject({
      type: 'inbound',
      channelId: 'ch-1',
      correlationId: 4n,
      flags: ChannelMessageFlags.MORE,
    })
    expect(transfer).toEqual([framed.buffer])
  })

  it('copies an inbound ciphertext on a SharedArrayBuffer, which a transfer cannot move', () => {
    const h = clientHarness()
    h.client.prepare()
    const shared = new Uint8Array(new SharedArrayBuffer(3))
    shared.set([7, 8, 9])

    h.client.forwardInbound('ch-1', create(ChannelMessageSchema, { channelId: 'ch-1', ciphertext: shared, correlationId: 4n }))

    const { command, transfer } = h.ports[0]!.posted.at(-1)!
    const ciphertext = (command as Extract<SessionCommand, { type: 'inbound' }>).ciphertext
    expect(ciphertext.buffer).toBeInstanceOf(ArrayBuffer)
    expect([...ciphertext]).toEqual([7, 8, 9])
    expect(transfer).toEqual([ciphertext.buffer])
  })

  it('throws from send with no worker, and does nothing for release, close, checkRekey and forwardInbound', () => {
    const h = clientHarness()

    expect(() => h.client.send('ch-1', 5, new Uint8Array(1), true)).toThrow('channel session worker is not running')
    h.client.release('ch-1', 5)
    h.client.close('ch-1')
    h.client.checkRekey()
    h.client.forwardInbound('ch-1', create(ChannelMessageSchema, { channelId: 'ch-1' }))

    expect(h.spawn).not.toHaveBeenCalled()
  })

  it('posts release, close and checkRekey to a live worker', () => {
    const h = clientHarness()
    h.client.prepare()

    h.client.release('ch-1', 5)
    h.client.close('ch-1')
    h.client.checkRekey()

    expect(h.ports[0]!.posted.slice(1).map(entry => entry.command)).toEqual([
      { type: 'release', channelId: 'ch-1', correlationId: 5 },
      { type: 'close', channelId: 'ch-1' },
      { type: 'checkRekey' },
    ])
  })
})

describe('ChannelSessionClient events', () => {
  it('routes wire, deliver, reassemblyFailed and sessionEnded to their deps', () => {
    const h = clientHarness()
    h.client.prepare()
    const port = h.ports[0]!
    const frame = new Uint8Array([1])
    const plaintext = new Uint8Array([2])

    port.emit({ type: 'wire', channelId: 'ch-1', frame })
    port.emit({
      type: 'deliver',
      channelId: 'ch-1',
      correlationId: 3,
      plaintext,
    })
    port.emit({
      type: 'reassemblyFailed',
      channelId: 'ch-1',
      correlationId: 4,
      message: 'too many incomplete chunked messages',
    })
    port.emit({
      type: 'sessionEnded',
      channelId: 'ch-1',
      error: {
        source: 'transport',
        message: 'rekey timeout',
        code: 0,
        fatal: false,
        disconnected: true,
      },
    })

    expect(h.deps.onWire).toHaveBeenCalledWith('ch-1', frame)
    expect(h.deps.onDeliver).toHaveBeenCalledWith('ch-1', 3, plaintext)
    expect(h.deps.onReassemblyFailed).toHaveBeenCalledWith('ch-1', 4, 'too many incomplete chunked messages')
    expect(h.deps.onSessionEnded).toHaveBeenCalledOnce()
    const [channelId, error] = h.deps.onSessionEnded.mock.calls[0]!
    expect(channelId).toBe('ch-1')
    expect(error).toBeInstanceOf(ChannelError)
    expect(error).toMatchObject({ source: 'transport', message: 'rekey timeout' })
  })

  it('keeps a dep that throws from escaping into the port', () => {
    const h = clientHarness()
    h.client.prepare()
    h.deps.onWire.mockImplementation(() => {
      throw new Error('relay broke')
    })
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(() => h.ports[0]!.emit({
      type: 'wire',
      channelId: 'ch-1',
      frame: new Uint8Array(1),
    })).not.toThrow()

    expect(errorSpy).toHaveBeenCalled()
    errorSpy.mockRestore()
  })
})

describe('ChannelSessionClient lost worker', () => {
  it('terminates a failed worker, rejects every pending request, and reports the loss once', async () => {
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const handshake = await begin(h)
    const first = h.client.ensureReady('ch-1')
    const second = h.client.finishHandshake(handshake, {
      channelId: 'ch-2',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    })
    const port = h.ports[0]!

    port.handlers!.failure('boom')
    port.handlers!.failure('boom again')

    expect(port.terminate).toHaveBeenCalledOnce()
    await expect(first).rejects.toMatchObject({ source: 'transport', message: 'channel session worker failed: boom' })
    await expect(second).rejects.toMatchObject({ message: 'channel session worker failed: boom' })
    expect(h.deps.onWorkerLost).toHaveBeenCalledOnce()
    expect(h.deps.onWorkerLost.mock.calls[0]![0]).toMatchObject({ message: 'channel session worker failed: boom' })

    // Events that the dead worker still queued go nowhere.
    port.emit({
      type: 'wire',
      channelId: 'ch-1',
      frame: new Uint8Array(1),
    })
    expect(h.deps.onWire).not.toHaveBeenCalled()

    // The next handshake spawns a new worker.
    await begin(h, 1)
    expect(h.spawn).toHaveBeenCalledTimes(2)
    errorSpy.mockRestore()
  })

  it('rejects finishHandshake for a handshake whose worker was lost, and posts it nowhere', async () => {
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const handshake = await begin(h)
    h.ports[0]!.handlers!.failure('boom')
    h.client.prepare()

    await expect(h.client.finishHandshake(handshake, {
      channelId: 'ch-1',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    }))
      .rejects
      .toMatchObject({ source: 'transport', message: 'channel session worker stopped' })
    expect(h.ports[1]!.commands('finishHandshake')).toEqual([])
    errorSpy.mockRestore()
  })

  it('treats the worker as stuck when a request runs past its budget', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    const h = clientHarness({ requestBudgetMs: 1_000 })
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    let outcome: unknown = 'pending'
    void h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys).catch((err: unknown) => {
      outcome = err
    })

    await vi.advanceTimersByTimeAsync(999)
    expect(outcome).toBe('pending')
    await vi.advanceTimersByTimeAsync(1)

    expect(outcome).toMatchObject({ source: 'transport', message: 'channel session worker stopped answering' })
    expect(h.ports[0]!.terminate).toHaveBeenCalledOnce()
    expect(h.deps.onWorkerLost).toHaveBeenCalledOnce()
    errorSpy.mockRestore()
  })

  it('defaults the request budget to SESSION_REQUEST_BUDGET_MS', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    void h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys).catch(() => {})

    await vi.advanceTimersByTimeAsync(SESSION_REQUEST_BUDGET_MS - 1)
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)

    expect(h.deps.onWorkerLost).toHaveBeenCalledOnce()
    errorSpy.mockRestore()
  })

  it('rejects only a request whose post throws, and keeps the worker', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    const h = clientHarness({ requestBudgetMs: 1_000 })
    h.client.prepare()
    const port = h.ports[0]!
    const post = port.postMessage.bind(port)
    port.postMessage = (command, transfer) => {
      if (command.type === 'ensureReady')
        throw new Error('the message could not be cloned')
      post(command, transfer)
    }

    await expect(h.client.ensureReady('ch-1'))
      .rejects
      .toMatchObject({ source: 'transport', message: 'post to the channel session worker: the message could not be cloned' })
    // The message never reached the worker, so its budget does not run either.
    await vi.advanceTimersByTimeAsync(1_000)

    expect(port.terminate).not.toHaveBeenCalled()
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
    await begin(h)
    expect(h.spawn).toHaveBeenCalledOnce()
  })

  it('throws a send that cannot be posted to its caller, and keeps the worker', () => {
    const h = clientHarness()
    h.client.prepare()
    const port = h.ports[0]!
    port.postMessage = () => {
      throw new Error('the message could not be cloned')
    }

    expect(() => h.client.send('ch-1', 5, new Uint8Array(1), true)).toThrow('the message could not be cloned')

    expect(port.terminate).not.toHaveBeenCalled()
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
  })

  // The worker no longer sees every command, so its state cannot be trusted.
  it('loses the worker when a command that waits for no answer cannot be posted', async () => {
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    h.client.prepare()
    const port = h.ports[0]!
    const ready = h.client.ensureReady('ch-1')
    const post = port.postMessage.bind(port)
    port.postMessage = (command, transfer) => {
      if (command.type === 'release')
        throw new Error('the port closed')
      post(command, transfer)
    }

    h.client.release('ch-1', 5)

    expect(port.terminate).toHaveBeenCalledOnce()
    expect(h.deps.onWorkerLost).toHaveBeenCalledOnce()
    expect(h.deps.onWorkerLost.mock.calls[0]![0]).toMatchObject({ source: 'transport', message: 'channel session worker failed: the port closed' })
    await expect(ready).rejects.toMatchObject({ message: 'channel session worker failed: the port closed' })
    h.client.prepare()
    expect(h.spawn).toHaveBeenCalledTimes(2)
    errorSpy.mockRestore()
  })

  it('still fails the pending requests and reports the loss when the worker cannot be terminated', async () => {
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    h.client.prepare()
    const port = h.ports[0]!
    port.terminate.mockImplementation(() => {
      throw new Error('already gone')
    })
    const ready = h.client.ensureReady('ch-1')

    expect(() => port.handlers!.failure('boom')).not.toThrow()

    expect(warnSpy).toHaveBeenCalledWith('[channel]', 'terminating the channel session worker failed', { error: 'already gone' })
    expect(h.deps.onWorkerLost).toHaveBeenCalledOnce()
    await expect(ready).rejects.toMatchObject({ message: 'channel session worker failed: boom' })
    errorSpy.mockRestore()
    warnSpy.mockRestore()
  })

  it('posts no abandon to the next worker for a handshake that a lost worker held', async () => {
    const h = clientHarness()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const handshake = await begin(h)
    h.ports[0]!.handlers!.failure('boom')
    h.client.prepare()

    h.client.abandonHandshake(handshake)

    expect(h.ports[0]!.commands('abandonHandshake')).toEqual([])
    expect(h.ports[1]!.commands('abandonHandshake')).toEqual([])
    errorSpy.mockRestore()
  })

  it('reports a failure to connect as a spawn failure, without a lost worker', async () => {
    const port = new FakePort()
    port.connect = () => {
      throw new Error('no port')
    }
    const h = clientHarness({
      spawn: () => port,
    })

    await expect(h.client.beginHandshake(EncryptionMode.POST_QUANTUM, keys))
      .rejects
      .toMatchObject({ source: 'transport', message: 'channel session worker could not start: no port' })
    expect(port.terminate).toHaveBeenCalledOnce()
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
  })
})

describe('ChannelSessionClient shutdown', () => {
  it('posts shutdown, rejects the pending requests, and spawns afresh on the next prepare', async () => {
    const h = clientHarness()
    h.client.prepare()
    const ready = h.client.ensureReady('ch-1')

    h.client.shutdown()

    expect(h.ports[0]!.posted.at(-1)!.command).toEqual({ type: 'shutdown' })
    await expect(ready).rejects.toMatchObject({ source: 'transport', message: 'channel session worker stopped' })
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()

    // A failure of the stopped worker no longer counts.
    h.ports[0]!.handlers!.failure('closing')
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()

    h.client.prepare()
    expect(h.spawn).toHaveBeenCalledTimes(2)
  })

  it('terminates a worker that does not take the shutdown, and still rejects the pending requests', async () => {
    const h = clientHarness()
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    h.client.prepare()
    const port = h.ports[0]!
    const ready = h.client.ensureReady('ch-1')
    port.postMessage = () => {
      throw new Error('the port closed')
    }

    expect(() => h.client.shutdown()).not.toThrow()

    expect(port.terminate).toHaveBeenCalledOnce()
    expect(warnSpy).toHaveBeenCalledWith('[channel]', 'the channel session worker did not take the shutdown; terminating it', { error: 'the port closed' })
    await expect(ready).rejects.toMatchObject({ source: 'transport', message: 'channel session worker stopped' })
    expect(h.deps.onWorkerLost).not.toHaveBeenCalled()
    warnSpy.mockRestore()
  })

  it('does nothing with no worker', () => {
    const h = clientHarness()
    expect(() => h.client.shutdown()).not.toThrow()
    expect(h.spawn).not.toHaveBeenCalled()
  })
})
