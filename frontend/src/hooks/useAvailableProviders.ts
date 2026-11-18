import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createMemo, on, onCleanup, untrack, useContext } from 'solid-js'
import { createStore } from 'solid-js/store'
import { workerAgent } from '~/api/workerRpc'
import { showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { createStableContext } from '~/lib/createStableContext'

interface WorkerProviders {
  providers: AgentProvider[] | undefined
  request: AbortController | undefined
}

/** Share the latest successful answer and the current request for each worker. */
export function createAvailableProvidersStore() {
  const [workers, setWorkers] = createStore<Record<string, WorkerProviders | undefined>>({})
  const observers = new Map<string, number>()
  let disposed = false

  onCleanup(() => {
    disposed = true
    for (const worker of Object.values(workers))
      worker?.request?.abort()
    observers.clear()
  })

  const scan = async (workerId: string, refresh: boolean): Promise<void> => {
    if (!workerId || disposed)
      return

    const previous = untrack(() => workers[workerId])
    const providers = previous?.providers
    if (previous?.request) {
      if (!refresh)
        return
      previous.request.abort()
    }
    const request = new AbortController()
    setWorkers(workerId, { providers, request })

    let discovered: AgentProvider[]
    try {
      const response = await workerAgent.listAvailableProviders(workerId, {}, { signal: request.signal })
      discovered = [...response.providers]
    }
    catch (error) {
      if (!request.signal.aborted) {
        setWorkers(workerId, { providers, request: undefined })
        showWarnToastUnlessDisconnected('Failed to load available agent providers', error)
      }
      return
    }

    if (!request.signal.aborted)
      setWorkers(workerId, { providers: discovered, request: undefined })
  }

  const observe = (workerId: string): (() => void) => {
    if (!workerId || disposed)
      return () => {}
    const count = observers.get(workerId) ?? 0
    observers.set(workerId, count + 1)
    // Revalidate when a worker gains its first consumer. Every other
    // consumer shares the request and keeps the last successful list visible.
    if (count === 0)
      void scan(workerId, false)

    return () => {
      if (disposed)
        return
      const remaining = (observers.get(workerId) ?? 1) - 1
      if (remaining > 0) {
        observers.set(workerId, remaining)
        return
      }
      observers.delete(workerId)
      const entry = untrack(() => workers[workerId])
      if (entry?.request) {
        entry.request.abort()
        setWorkers(workerId, { providers: entry.providers, request: undefined })
      }
    }
  }

  return {
    providers: (workerId: string) => workers[workerId]?.providers,
    loading: (workerId: string) => workers[workerId]?.request !== undefined,
    observe,
    refresh: (workerId: string) => scan(workerId, true),
  }
}

export const AvailableProvidersContext = createStableContext<ReturnType<typeof createAvailableProvidersStore>>('hooks/useAvailableProviders')

/** Read and refresh the list for the worker this surface targets. */
export function useAvailableProviders(
  workerId: Accessor<string>,
  enabled: Accessor<boolean> = () => true,
  providerStore?: ReturnType<typeof createAvailableProvidersStore>,
) {
  const store = providerStore ?? useContext(AvailableProvidersContext) ?? createAvailableProvidersStore()
  const selectedWorkerId = createMemo(workerId)
  const shouldLoad = createMemo(enabled)
  createEffect(on([selectedWorkerId, shouldLoad], ([id, open]) => {
    if (open && id)
      onCleanup(store.observe(id))
  }))

  return {
    providers: () => store.providers(selectedWorkerId()),
    loading: () => store.loading(selectedWorkerId()),
    refresh: () => store.refresh(selectedWorkerId()),
  }
}
