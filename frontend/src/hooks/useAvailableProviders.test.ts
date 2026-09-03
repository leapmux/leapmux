import type { ListAvailableProvidersResponse } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred, flush } from '~/test-support/async'

const listAvailableProviders = vi.fn<(workerId: string, opts?: { signal?: AbortSignal }) => Promise<ListAvailableProvidersResponse>>()

vi.mock('~/api/workerRpc', () => ({
  listAvailableProviders: (workerId: string, opts?: { signal?: AbortSignal }) =>
    listAvailableProviders(workerId, opts),
}))

const { useAvailableProviders } = await import('./useAvailableProviders')

function providersResp(providers: AgentProvider[]): ListAvailableProvidersResponse {
  return {
    $typeName: 'leapmux.v1.ListAvailableProvidersResponse',
    providers,
  } as ListAvailableProvidersResponse
}

beforeEach(() => {
  listAvailableProviders.mockReset()
})

describe('useAvailableProviders', () => {
  it('does not fetch while the source returns null', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        await flush()
        expect(listAvailableProviders).not.toHaveBeenCalled()
        expect(hook.loading()).toBe(false)
        expect(hook.providers()).toBeUndefined()
        dispose()
        done()
      })
    })
  })

  it('fetches once the source returns args and populates the list', async () => {
    listAvailableProviders.mockResolvedValueOnce(providersResp([AgentProvider.CLAUDE_CODE, AgentProvider.CODEX]))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        setSource({ workerId: 'A' })
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)
        expect(listAvailableProviders.mock.calls[0][0]).toBe('A')
        expect(hook.providers()).toEqual([AgentProvider.CLAUDE_CODE, AgentProvider.CODEX])
        expect(hook.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  // `[]` is an answer ("this Worker has none"); `undefined` is the absence of
  // one. A caller that conflated them would tell the user a reachable Worker
  // had no providers while the scan was still in flight.
  it('reports an empty list as an answer, not as absence', async () => {
    listAvailableProviders.mockResolvedValueOnce(providersResp([]))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        setSource({ workerId: 'A' })
        await flush()
        expect(hook.providers()).toEqual([])
        dispose()
        done()
      })
    })
  })

  it('latches: source flipping null and back does not re-fetch and keeps the list', async () => {
    listAvailableProviders.mockResolvedValueOnce(providersResp([AgentProvider.CLAUDE_CODE]))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        setSource({ workerId: 'A' })
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)

        // The branch menu closes: the gate drops but the answer stays, so
        // re-opening the same row costs no round trip.
        setSource(null)
        await flush()
        expect(hook.providers()).toEqual([AgentProvider.CLAUDE_CODE])
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)

        setSource({ workerId: 'A' })
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('re-fetches on a workerId change and clears the previous worker\'s list first', async () => {
    const second = deferred<ListAvailableProvidersResponse>()
    listAvailableProviders
      .mockResolvedValueOnce(providersResp([AgentProvider.CLAUDE_CODE]))
      .mockImplementationOnce(() => second.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        setSource({ workerId: 'A' })
        await flush()
        expect(hook.providers()).toEqual([AgentProvider.CLAUDE_CODE])

        // Worker B may not have A's providers, so the stale list must go
        // before the new answer lands -- otherwise the menu offers a provider
        // this machine cannot run.
        setSource({ workerId: 'B' })
        await flush()
        expect(hook.providers()).toBeUndefined()
        expect(hook.loading()).toBe(true)

        second.resolve(providersResp([AgentProvider.CODEX]))
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(2)
        expect(hook.providers()).toEqual([AgentProvider.CODEX])
        dispose()
        done()
      })
    })
  })

  it('source identity churn with a stable workerId does not refire the fetch', async () => {
    listAvailableProviders.mockResolvedValueOnce(providersResp([AgentProvider.CLAUDE_CODE]))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [tick, setTick] = createSignal(0)
        // A fresh object on every read, exactly as a real caller builds one.
        const source = () => {
          tick()
          return { workerId: 'A' }
        }
        useAvailableProviders(source)
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)
        setTick(1)
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('reports the failure, clears the list, and lets the same worker retry', async () => {
    const onError = vi.fn()
    listAvailableProviders
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce(providersResp([AgentProvider.CODEX]))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source, onError)
        setSource({ workerId: 'A' })
        await flush()
        expect(onError).toHaveBeenCalledTimes(1)
        expect(hook.providers()).toBeUndefined()
        expect(hook.loading()).toBe(false)

        // The failure left `lastLoadedWorkerId` alone, so a manual retry
        // against the SAME worker still reaches the RPC.
        await hook.refresh()
        await flush()
        expect(listAvailableProviders).toHaveBeenCalledTimes(2)
        expect(hook.providers()).toEqual([AgentProvider.CODEX])
        dispose()
        done()
      })
    })
  })

  it('refresh is a no-op while the source is null', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        await hook.refresh()
        await flush()
        expect(listAvailableProviders).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('loading flips true during the in-flight fetch and false on resolve', async () => {
    const d = deferred<ListAvailableProvidersResponse>()
    listAvailableProviders.mockImplementationOnce(() => d.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [source, setSource] = createSignal<{ workerId: string } | null>(null)
        const hook = useAvailableProviders(source)
        setSource({ workerId: 'A' })
        await flush()
        expect(hook.loading()).toBe(true)
        d.resolve(providersResp([AgentProvider.CLAUDE_CODE]))
        await flush()
        expect(hook.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })
})
