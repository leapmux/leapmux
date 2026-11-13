import type { DescService, MessageInitShape } from '@bufbuild/protobuf'
import type { ChannelTransport } from '~/lib/channel'
import { create, getOption } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MLKEM_PUBLIC_KEY_SIZE, SLHDSA_PUBLIC_KEY_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { host, ServiceHost } from '~/generated/proto/leapmux/v1/access_pb'
import { WorkerAgentInputService, WorkerAgentService } from '~/generated/proto/leapmux/v1/agent_pb'
import { EncryptionMode, GetWorkerHandshakeParamsResponseSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { WorkerFileService } from '~/generated/proto/leapmux/v1/file_pb'
import { WorkerGitService } from '~/generated/proto/leapmux/v1/git_pb'
import { WorkerTerminalService } from '~/generated/proto/leapmux/v1/terminal_pb'
import { WorkerPrivateService } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { WorkerTabService } from '~/generated/proto/leapmux/v1/worker_tab_pb'
import { ChannelError } from '~/lib/channelError'

// The retry lives inside `callWorker`, which is private — so these drive it
// through a real client method and control the one seam beneath it.
const callWorkerMock = vi.hoisted(() => vi.fn())

// The channel transport the module hands its ChannelManager, and the Connect
// transport beneath the hub client that the channel transport calls.
const seams = vi.hoisted(() => ({
  channelTransport: undefined as unknown,
  channelOpts: undefined as unknown,
  unary: vi.fn(),
}))

vi.mock('~/lib/channel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/channel')>()
  return {
    ...actual,
    ChannelManager: class {
      callWorker = callWorkerMock
      setConfirmKeyPin = () => () => {}
      constructor(transport: unknown, opts: unknown) {
        seams.channelTransport = transport
        seams.channelOpts = opts
      }
    },
  }
})

vi.mock('~/api/transport', () => ({
  transport: { unary: seams.unary },
  apiLoadingTimeoutMs: () => 30_000,
}))

function unavailable() {
  return new ChannelError('rpc', 'provider scan did not finish; retry', { code: Code.Unavailable })
}

beforeEach(() => {
  callWorkerMock.mockReset()
  vi.useRealTimers()
})

describe('callWorker retries an Unavailable reply', () => {
  // The worker declares Unavailable as "the caller should retry": an
  // incomplete provider scan, a subagent registry that is not loaded, and
  // a steering message that arrives too early all answer with it. Handling
  // it in the layer that knows the code covers every one of those sites.
  it('retries and returns the later answer', async () => {
    vi.useFakeTimers()
    const { workerAgent } = await import('./workerRpc')
    callWorkerMock
      .mockRejectedValueOnce(unavailable())
      .mockResolvedValueOnce({
        providers: [1],
      })

    const p = workerAgent.listAvailableProviders('w-1')
    await vi.advanceTimersByTimeAsync(200)
    await expect(p).resolves.toEqual({
      providers: [1],
    })
    expect(callWorkerMock).toHaveBeenCalledTimes(2)
  })

  it('gives up after the budget and rejects with the last error', async () => {
    vi.useFakeTimers()
    const { workerAgent, UNAVAILABLE_MAX_RETRIES } = await import('./workerRpc')
    const err = unavailable()
    callWorkerMock.mockRejectedValue(err)

    const p = workerAgent.listAvailableProviders('w-1')
    const settled = expect(p).rejects.toBe(err)
    await vi.advanceTimersByTimeAsync(200 + 400 + 800)
    await settled
    expect(callWorkerMock).toHaveBeenCalledTimes(UNAVAILABLE_MAX_RETRIES + 1)
  })

  // `source` separates the worker's own refusal from a transport failure
  // that happens to carry the same numeric code. Only the former means
  // "ask again"; retrying the latter would hide a dead connection.
  it('does not retry a transport failure with the same code', async () => {
    const { workerAgent } = await import('./workerRpc')
    const err = new ChannelError('transport', 'socket closed', { code: Code.Unavailable })
    callWorkerMock.mockRejectedValue(err)

    await expect(workerAgent.listAvailableProviders('w-1')).rejects.toBe(err)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('does not retry a different code', async () => {
    const { workerAgent } = await import('./workerRpc')
    const err = new ChannelError('rpc', 'no', { code: Code.FailedPrecondition })
    callWorkerMock.mockRejectedValue(err)

    await expect(workerAgent.listAvailableProviders('w-1')).rejects.toBe(err)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('stops immediately when the caller aborts', async () => {
    vi.useFakeTimers()
    const { workerAgent } = await import('./workerRpc')
    callWorkerMock.mockRejectedValue(unavailable())
    const abort = new AbortController()

    const p = workerAgent.listAvailableProviders('w-1', {}, { signal: abort.signal })
    // The abort ends the backoff, so the call rejects with the abort and not with the Unavailable reply.
    const settled = expect(p).rejects.toMatchObject({ name: 'AbortError' })
    // Let the first attempt fail, so that its backoff timer is the one pending timer.
    await vi.advanceTimersByTimeAsync(0)
    expect(vi.getTimerCount()).toBe(1)
    // Abort mid-backoff: the pending delay must not outlive the caller.
    abort.abort()
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(2000)
    await settled
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  // The other abort path: the caller aborts while the attempt is in flight. The check before the backoff then
  // rethrows the reply of the attempt, and no backoff starts.
  it('rethrows the reply without a backoff when the caller aborts during the attempt', async () => {
    vi.useFakeTimers()
    const { workerAgent } = await import('./workerRpc')
    const err = unavailable()
    callWorkerMock.mockRejectedValue(err)
    const abort = new AbortController()

    const p = workerAgent.listAvailableProviders('w-1', {}, { signal: abort.signal })
    abort.abort()
    await expect(p).rejects.toBe(err)
    expect(vi.getTimerCount()).toBe(0)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('makes exactly one call when the first attempt succeeds', async () => {
    const { workerAgent } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({ providers: [] })

    await expect(workerAgent.listAvailableProviders('w-1')).resolves.toEqual({ providers: [] })
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })
})

function isService(value: unknown): value is DescService {
  return typeof value === 'object' && value !== null && 'kind' in value && value.kind === 'service'
}

describe('worker service clients', () => {
  async function clientsByService(): Promise<ReadonlyArray<[string, DescService, Record<string, unknown>]>> {
    const { workerAgent, workerAgentInput, workerFile, workerGit, workerPrivate, workerSystem, workerTab, workerTerminal } = await import('./workerRpc')
    return [
      ['workerAgent', WorkerAgentService, workerAgent],
      ['workerAgentInput', WorkerAgentInputService, workerAgentInput],
      ['workerFile', WorkerFileService, workerFile],
      ['workerGit', WorkerGitService, workerGit],
      ['workerPrivate', WorkerPrivateService, workerPrivate],
      ['workerSystem', WorkerSystemService, workerSystem],
      ['workerTab', WorkerTabService, workerTab],
      ['workerTerminal', WorkerTerminalService, workerTerminal],
    ]
  }

  // A method that pairs its name with another method's descriptor sends a
  // request the worker decodes as the wrong message. Each client method carries
  // the local name of its method, so the table derives itself.
  it('routes each method to its own descriptor, through callWorker', async () => {
    const routed: string[] = []
    for (const [name, service, client] of await clientsByService()) {
      for (const method of service.methods) {
        const call = client[method.localName]
        if (method.methodKind !== 'unary')
          continue
        callWorkerMock.mockReset().mockResolvedValue({})
        await (call as (workerId: string, req: object) => Promise<unknown>)('w-1', {})
        expect(callWorkerMock, `${name}.${method.localName}`).toHaveBeenCalledExactlyOnceWith('w-1', method, {}, undefined)
        routed.push(method.localName)
      }
    }
    expect(routed).toEqual(expect.arrayContaining([
      'closeTab',
      'cleanupWorkspace',
      'renameTerminal',
      'getWorkerSystemInfo',
      'listFilesystemRoots',
      'getTabPayload',
      'registerTabPayload',
    ]))
  })

  it('holds the unary methods of its service and no other', async () => {
    for (const [name, service, client] of await clientsByService()) {
      expect(Object.keys(client).sort(), name).toEqual(
        service.methods.filter(method => method.methodKind === 'unary').map(method => method.localName).sort(),
      )
    }
  })

  // A new worker service must get a client, or no browser code can call it. A
  // stream-only service has no unary method to offer, and watchEventsViaChannel
  // serves the one that exists. The Go tunnel client of the desktop app is the
  // only caller of WorkerTunnelService, so that service has no browser client.
  it('covers every worker-hosted service that declares a unary method, except the tunnel', async () => {
    const generated = Object.values(import.meta.glob('~/generated/proto/leapmux/v1/*_pb.ts', { eager: true }))
    const services = generated.flatMap(module => Object.values(module as Record<string, unknown>)).filter(isService)
    expect(services.length, 'the glob must reach the generated services').toBeGreaterThan(0)
    const callable = services.filter(service => getOption(service, host) === ServiceHost.WORKER
      && service.typeName !== 'leapmux.v1.WorkerTunnelService'
      && service.methods.some(method => method.methodKind === 'unary'))

    const covered = (await clientsByService()).map(([, service]) => service.typeName)

    expect(callable.map(service => service.typeName).sort()).toEqual(covered.sort())
  })

  // A caller hands a method on as a plain function, as agentInputQueueOperations
  // does, so a method must not rely on `this`.
  it('keeps a method working when a caller detaches it from the client', async () => {
    const { workerFile } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})
    const { statFile } = workerFile

    await statFile('w-1', { path: '/repo' })

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerFileService.method.statFile, { path: '/repo' }, undefined)
  })

  // Each method is an own property, so a test can spread a client into a mock.
  it('spreads into a plain object that holds every method', async () => {
    const { workerGit } = await import('./workerRpc')

    expect({ ...workerGit }).toEqual(workerGit)
  })

  it('passes the request of a closeTab through unchanged', async () => {
    const { workerTab } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})
    const request = {
      tab: { tabType: TabType.AGENT, tabId: 't-1' },
    }

    await workerTab.closeTab('w-1', request)

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerTabService.method.closeTab, request, undefined)
  })

  it('returns the response of callWorker', async () => {
    const { workerFile } = await import('./workerRpc')
    const response = { entries: [] }
    callWorkerMock.mockResolvedValue(response)

    await expect(workerFile.statFile('w-1', { path: '/repo' })).resolves.toBe(response)
  })

  // Opening an agent starts a process, so it waits as long as the loading UI.
  // That is the default timeout of the channel manager, so the client sets none
  // of its own.
  it('gives openAgent the loading timeout through the manager default', async () => {
    const { workerAgent } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})

    await workerAgent.openAgent('w-1', {})

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerAgentService.method.openAgent, {}, undefined)
    expect((seams.channelOpts as { rpcTimeoutFn: () => number }).rpcTimeoutFn()).toBe(30_000)
  })

  // The keys of every channel live in the channel session worker, never on the
  // page's own thread.
  it('gives the manager the spawn of the channel session worker', async () => {
    await import('./workerRpc')
    const { spawnChannelSessionWorker } = await import('~/lib/channelSessionClient')

    expect((seams.channelOpts as { spawnSessionWorker: unknown }).spawnSessionWorker).toBe(spawnChannelSessionWorker)
  })

  // Every method takes the same options, so a caller can stop any call.
  it('passes the abort signal and the timeout of any method through', async () => {
    const { workerFile } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})
    const signal = new AbortController().signal

    await workerFile.statFile('w-1', { path: '/repo' }, { signal, timeoutMs: 5_000 })

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerFileService.method.statFile, { path: '/repo' }, { signal, timeoutMs: 5_000 })
  })

  it('sends an empty request for a method whose request has no field', async () => {
    const { workerAgent, workerFile, workerSystem, workerTerminal } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})
    const signal = new AbortController().signal

    await workerSystem.getWorkerSystemInfo('w-1')
    await workerFile.listFilesystemRoots('w-1')
    await workerTerminal.listAvailableShells('w-1')
    await workerAgent.listAvailableProviders('w-1', {}, { signal })
    await workerAgent.listAvailableProviders('w-1', undefined, { signal })

    expect(callWorkerMock.mock.calls).toEqual([
      [
        'w-1',
        WorkerSystemService.method.getWorkerSystemInfo,
        {},
        undefined,
      ],
      [
        'w-1',
        WorkerFileService.method.listFilesystemRoots,
        {},
        undefined,
      ],
      [
        'w-1',
        WorkerTerminalService.method.listAvailableShells,
        {},
        undefined,
      ],
      [
        'w-1',
        WorkerAgentService.method.listAvailableProviders,
        {},
        { signal },
      ],
      [
        'w-1',
        WorkerAgentService.method.listAvailableProviders,
        {},
        { signal },
      ],
    ])
  })

  // `tsc --noEmit` checks these lines: an unused @ts-expect-error fails the
  // compile, so each line below proves that the compiler refuses the call.
  describe('the mapped type', () => {
    it('refuses a streaming method', async () => {
      const { workerPrivate } = await import('./workerRpc')

      // @ts-expect-error A stream has a handle, so it is no client method.
      expect(workerPrivate.watchWorkerPrivateEvents).toBeUndefined()
    })

    it('refuses an unknown request field', async () => {
      const { workerFile } = await import('./workerRpc')
      callWorkerMock.mockResolvedValue({})

      // @ts-expect-error StatFileRequest has no such field.
      await workerFile.statFile('w-1', { path: '/repo', noSuchField: true })
    })

    it('refuses an option in the place of the request of a request-less method', async () => {
      const { workerAgent } = await import('./workerRpc')
      callWorkerMock.mockResolvedValue({})

      // @ts-expect-error The options come third, so `signal` is an unknown request field.
      await workerAgent.listAvailableProviders('w-1', { signal: new AbortController().signal })
    })

    it('requires the request of a method whose request has a field', async () => {
      const { workerFile } = await import('./workerRpc')
      callWorkerMock.mockResolvedValue({})

      // @ts-expect-error StatFileRequest has fields, so the request is required.
      await workerFile.statFile('w-1')

      expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerFileService.method.statFile, {}, undefined)
    })

    it('types the response from the descriptor', async () => {
      const { workerSystem } = await import('./workerRpc')
      callWorkerMock.mockResolvedValue({ name: 'host' })

      const response = await workerSystem.getWorkerSystemInfo('w-1')

      // @ts-expect-error GetWorkerSystemInfoResponse has no such field.
      expect(response.noSuchField).toBeUndefined()
      expect(response.name).toBe('host')
    })
  })
})

describe('the browser channel transport', () => {
  async function channelTransport(): Promise<ChannelTransport> {
    await import('./workerRpc')
    if (seams.channelTransport === undefined)
      throw new Error('The module must hand its ChannelManager a transport.')
    return seams.channelTransport as ChannelTransport
  }

  function handshakeReply(init: MessageInitShape<typeof GetWorkerHandshakeParamsResponseSchema>) {
    return {
      message: create(GetWorkerHandshakeParamsResponseSchema, init),
      header: new Headers(),
      trailer: new Headers(),
    }
  }

  beforeEach(() => {
    seams.unary.mockReset()
  })

  /** A key set of the sizes the handshake takes, each key filled with one byte value. */
  function validKeys() {
    return {
      x25519: new Uint8Array(X25519_PUBLIC_KEY_SIZE).fill(1),
      mlkem: new Uint8Array(MLKEM_PUBLIC_KEY_SIZE).fill(2),
      slhdsa: new Uint8Array(SLHDSA_PUBLIC_KEY_SIZE).fill(3),
    }
  }

  // The hub reports one WorkerPublicKeys message from the worker's hello.
  it('reads the worker keys from the nested key message', async () => {
    const transport = await channelTransport()
    const keys = validKeys()
    seams.unary.mockResolvedValueOnce(handshakeReply({
      keys,
      encryptionMode: EncryptionMode.POST_QUANTUM,
    }))

    await expect(transport.getWorkerHandshakeParams('w-1')).resolves.toEqual({
      keys: {
        x25519PublicKey: keys.x25519,
        mlkemPublicKey: keys.mlkem,
        slhdsaPublicKey: keys.slhdsa,
      },
      encryptionMode: EncryptionMode.POST_QUANTUM,
    })
    expect(seams.unary.mock.calls[0]?.[0]?.name).toBe('GetWorkerHandshakeParams')
    expect(seams.unary.mock.calls[0]?.[4]).toMatchObject({ workerId: 'w-1' })
  })

  // Every mode takes all three keys: the hub refuses a hello without them, and
  // the pin covers all three.
  it('reads the keys in classic mode too', async () => {
    const transport = await channelTransport()
    seams.unary.mockResolvedValueOnce(handshakeReply({
      keys: validKeys(),
      encryptionMode: EncryptionMode.CLASSIC,
    }))

    await expect(transport.getWorkerHandshakeParams('w-1')).resolves.toMatchObject({
      encryptionMode: EncryptionMode.CLASSIC,
    })
  })

  // An absent key set used to become three empty keys. The pin store then
  // compared an empty fingerprint with the pin, and a pinned worker showed a
  // "key differs" prompt with a zero-byte fingerprint; accepting it started a
  // handshake with an empty X25519 key.
  it('refuses a reply that carries no key message, before any pin decision', async () => {
    const transport = await channelTransport()
    seams.unary.mockResolvedValueOnce(handshakeReply({
      encryptionMode: EncryptionMode.POST_QUANTUM,
    }))

    const refusal = await transport.getWorkerHandshakeParams('w-1').catch((err: unknown) => err)

    expect(refusal).toBeInstanceOf(ChannelError)
    expect((refusal as ChannelError).message).toBe('open channel: the hub sent no public keys for worker w-1')
    expect((refusal as ChannelError).disconnected, 'a malformed reply is not a dropped link').toBe(false)
  })

  for (const [name, key, size] of [
    ['X25519', 'x25519', X25519_PUBLIC_KEY_SIZE],
    ['ML-KEM', 'mlkem', MLKEM_PUBLIC_KEY_SIZE],
    ['SLH-DSA', 'slhdsa', SLHDSA_PUBLIC_KEY_SIZE],
  ] as const) {
    for (const [shape, length] of [
      ['an empty', 0],
      ['a short', size - 1],
      ['a long', size + 1],
    ] as const) {
      it(`refuses ${shape} ${name} key`, async () => {
        const transport = await channelTransport()
        seams.unary.mockResolvedValueOnce(handshakeReply({
          keys: {
            ...validKeys(),
            [key]: new Uint8Array(length),
          },
          encryptionMode: EncryptionMode.POST_QUANTUM,
        }))

        const refusal = await transport.getWorkerHandshakeParams('w-1').catch((err: unknown) => err)

        expect(refusal).toBeInstanceOf(ChannelError)
        expect((refusal as ChannelError).message).toBe(`open channel: the ${name} public key that the hub sent for worker w-1 is ${length} bytes, not ${size}`)
      })
    }
  }
})
