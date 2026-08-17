import { Code } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ChannelError } from '~/lib/channelError'

// The retry lives inside `callWorker`, which is private — so these drive it
// through a real wrapper and control the one seam beneath it.
const callWorkerMock = vi.hoisted(() => vi.fn())

vi.mock('~/lib/channel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/channel')>()
  return {
    ...actual,
    ChannelManager: class {
      callWorker = callWorkerMock
      setConfirmKeyPin = () => () => {}
    },
  }
})

vi.mock('~/api/transport', () => ({
  transport: {},
  apiLoadingTimeoutMs: () => 30_000,
}))

function unavailable() {
  return new ChannelError('rpc', 'provider scan did not finish; retry', Code.Unavailable)
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
      .mockResolvedValueOnce({ providers: [1] })

    const p = listAvailableProviders('w-1')
    await vi.advanceTimersByTimeAsync(200)
    await expect(p).resolves.toEqual({ providers: [1] })
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
    const err = new ChannelError('transport', 'socket closed', Code.Unavailable)
    callWorkerMock.mockRejectedValue(err)

    await expect(listAvailableProviders('w-1')).rejects.toBe(err)
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('does not retry a different code', async () => {
    const { listAvailableProviders } = await import('./workerRpc')
    const err = new ChannelError('rpc', 'no', Code.FailedPrecondition)
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
    const settled = expect(p).rejects.toBeDefined()
    // Abort mid-backoff: the pending delay must not outlive the caller.
    abort.abort()
    await vi.advanceTimersByTimeAsync(2000)
    await settled
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })

  it('makes exactly one call when the first attempt succeeds', async () => {
    const { listAvailableProviders } = await import('./workerRpc')
    callWorkerMock.mockResolvedValue({ providers: [] })

    await expect(listAvailableProviders('w-1')).resolves.toEqual({ providers: [] })
    expect(callWorkerMock).toHaveBeenCalledTimes(1)
  })
})
