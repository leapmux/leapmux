import type { DescService, MessageInitShape } from '@bufbuild/protobuf'
import type { ChannelTransport } from '~/lib/channel'
import { create } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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
// through a real wrapper and control the one seam beneath it.
const callWorkerMock = vi.hoisted(() => vi.fn())

// The channel transport the module hands its ChannelManager, and the Connect
// transport beneath the hub client that the channel transport calls.
const seams = vi.hoisted(() => ({
  channelTransport: undefined as unknown,
  unary: vi.fn(),
}))

vi.mock('~/lib/channel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/channel')>()
  return {
    ...actual,
    ChannelManager: class {
      callWorker = callWorkerMock
      setConfirmKeyPin = () => () => {}
      constructor(transport: unknown) {
        seams.channelTransport = transport
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
    const { listAvailableProviders } = await import('./workerRpc')
    callWorkerMock
      .mockRejectedValueOnce(unavailable())
      .mockResolvedValueOnce({
        providers: [1],
      })

    const p = listAvailableProviders('w-1')
    await vi.advanceTimersByTimeAsync(200)
    await expect(p).resolves.toEqual({
      providers: [1],
    })
    expect(callWorkerMock).toHaveBeenCalledTimes(2)
  })

  it('gives up after the budget and rejects with the last error', async () => {
    vi.useFakeTimers()
    const { listAvailableProviders, UNAVAILABLE_MAX_RETRIES } = await import('./workerRpc')
    const err = unavailable()
    callWorkerMock.mockRejectedValue(err)

    const p = listAvailableProviders('w-1')
    const settled = expect(p).rejects.toBe(err)
    await vi.advanceTimersByTimeAsync(200 + 400 + 800)
    await settled
    expect(callWorkerMock).toHaveBeenCalledTimes(UNAVAILABLE_MAX_RETRIES + 1)
  })

  // `source` separates the worker's own refusal from a transport failure
  // that happens to carry the same numeric code. Only the former means
  // "ask again"; retrying the latter would hide a dead connection.
  it('does not retry a transport failure with the same code', async () => {
    const { listAvailableProviders } = await import('./workerRpc')
    const err = new ChannelError('transport', 'socket closed', { code: Code.Unavailable })
    callWorkerMock.mockRejectedValue(err)

    await expect(listAvailableProviders('w-1')).rejects.toBe(err)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('does not retry a different code', async () => {
    const { listAvailableProviders } = await import('./workerRpc')
    const err = new ChannelError('rpc', 'no', { code: Code.FailedPrecondition })
    callWorkerMock.mockRejectedValue(err)

    await expect(listAvailableProviders('w-1')).rejects.toBe(err)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('stops immediately when the caller aborts', async () => {
    vi.useFakeTimers()
    const { listAvailableProviders } = await import('./workerRpc')
    callWorkerMock.mockRejectedValue(unavailable())
    const abort = new AbortController()

    const p = listAvailableProviders('w-1', { signal: abort.signal })
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
    const { listAvailableProviders } = await import('./workerRpc')
    const err = unavailable()
    callWorkerMock.mockRejectedValue(err)
    const abort = new AbortController()

    const p = listAvailableProviders('w-1', { signal: abort.signal })
    abort.abort()
    await expect(p).rejects.toBe(err)
    expect(vi.getTimerCount()).toBe(0)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('makes exactly one call when the first attempt succeeds', async () => {
    const { listAvailableProviders } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({ providers: [] })

    await expect(listAvailableProviders('w-1')).resolves.toEqual({ providers: [] })
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })
})

// Every worker service a browser calls through the E2EE channel.
const WORKER_SERVICES: readonly DescService[] = [
  WorkerAgentService,
  WorkerAgentInputService,
  WorkerFileService,
  WorkerGitService,
  WorkerTerminalService,
  WorkerPrivateService,
  WorkerSystemService,
  WorkerTabService,
]

describe('worker RPC wrappers', () => {
  // A wrapper that pairs its name with another method's descriptor sends a
  // request the worker decodes as the wrong message. Each exported wrapper
  // carries the local name of its method, so the table derives itself.
  it('routes each wrapper to the descriptor of its own method', async () => {
    const workerRpc = await import('./workerRpc') as unknown as Record<string, unknown>
    const routed: string[] = []
    for (const service of WORKER_SERVICES) {
      for (const method of service.methods) {
        const wrapper = workerRpc[method.localName]
        if (method.methodKind !== 'unary' || typeof wrapper !== 'function')
          continue
        callWorkerMock.mockReset().mockResolvedValue({})
        await (wrapper as (workerId: string, req: object) => Promise<unknown>)('w-1', {})
        expect(callWorkerMock, method.localName).toHaveBeenCalledOnce()
        expect(callWorkerMock.mock.calls[0]?.[0], method.localName).toBe('w-1')
        expect(callWorkerMock.mock.calls[0]?.[1], method.localName).toBe(method)
        routed.push(method.localName)
      }
    }
    // The wrappers this change added or renamed are among those checked.
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

  it('passes the request of a closeTab through unchanged', async () => {
    const { closeTab } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})
    const request = {
      tab: { tabType: TabType.AGENT, tabId: 't-1' },
    }

    await closeTab('w-1', request)

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerTabService.method.closeTab, request, undefined)
  })

  // Opening an agent starts a process, so it waits as long as the loading UI.
  it('gives openAgent the loading timeout', async () => {
    const { openAgent } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({})

    await openAgent('w-1', {})

    expect(callWorkerMock).toHaveBeenCalledWith('w-1', WorkerAgentService.method.openAgent, {}, { timeoutMs: 30_000 })
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

  // The hub reports one WorkerPublicKeys message from the worker's hello.
  it('reads the worker keys from the nested key message', async () => {
    const transport = await channelTransport()
    seams.unary.mockResolvedValueOnce(handshakeReply({
      keys: {
        x25519: new Uint8Array([1]),
        mlkem: new Uint8Array([2]),
        slhdsa: new Uint8Array([3]),
      },
      encryptionMode: EncryptionMode.POST_QUANTUM,
    }))

    await expect(transport.getWorkerHandshakeParams('w-1')).resolves.toEqual({
      keys: {
        x25519PublicKey: new Uint8Array([1]),
        mlkemPublicKey: new Uint8Array([2]),
        slhdsaPublicKey: new Uint8Array([3]),
      },
      encryptionMode: EncryptionMode.POST_QUANTUM,
    })
    expect(seams.unary.mock.calls[0]?.[0]?.name).toBe('GetWorkerHandshakeParams')
    expect(seams.unary.mock.calls[0]?.[4]).toMatchObject({ workerId: 'w-1' })
  })

  // An absent key message yields empty keys rather than a crash on an
  // undefined field.
  it('reports empty keys when the reply carries no key message', async () => {
    const transport = await channelTransport()
    seams.unary.mockResolvedValueOnce(handshakeReply({
      encryptionMode: EncryptionMode.CLASSIC,
    }))

    await expect(transport.getWorkerHandshakeParams('w-1')).resolves.toEqual({
      keys: {
        x25519PublicKey: new Uint8Array(),
        mlkemPublicKey: new Uint8Array(),
        slhdsaPublicKey: new Uint8Array(),
      },
      encryptionMode: EncryptionMode.CLASSIC,
    })
  })
})
