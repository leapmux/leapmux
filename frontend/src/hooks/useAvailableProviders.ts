import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { batch, createEffect, createMemo, on, onCleanup, untrack, useContext } from 'solid-js'
import { createStore } from 'solid-js/store'
import { workerAgent } from '~/api/workerRpc'
import { showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { createStableContext } from '~/lib/createStableContext'

interface WorkerProviders {
  providers: AgentProvider[] | undefined
  loading: boolean
}

/** Share the latest successful answer and the current request for each worker. */
export function createAvailableProvidersStore() {
  const [workers, setWorkers] = createStore<Record<string, WorkerProviders | undefined>>({})
  const requests = new Map<string, AbortController>()
  let disposed = false

  onCleanup(() => {
    disposed = true
    for (const request of requests.values())
      request.abort()
    requests.clear()
  })

  const scan = async (workerId: string, refresh: boolean): Promise<void> => {
    if (!workerId || disposed)
      return

    const previous = untrack(() => workers[workerId]?.providers)
    const inFlight = requests.get(workerId)
    if (!refresh && (inFlight || previous !== undefined))
      return

    inFlight?.abort()
    const request = new AbortController()
    requests.set(workerId, request)
    setWorkers(workerId, { providers: previous, loading: true })

    try {
      const response = await workerAgent.listAvailableProviders(workerId, {}, { signal: request.signal })
      if (!request.signal.aborted) {
        setWorkers(workerId, {
          providers: [...response.providers],
          loading: false,
        })
      }
    }
    catch (error) {
      if (request.signal.aborted)
        return
      batch(() => {
        setWorkers(workerId, { providers: previous, loading: false })
        showWarnToastUnlessDisconnected('Failed to load available agent providers', error)
      })
    }
    finally {
      if (requests.get(workerId) === request)
        requests.delete(workerId)
    }
  }

  return {
    providers: (workerId: string) => workers[workerId]?.providers,
    loading: (workerId: string) => workers[workerId]?.loading ?? false,
    load: (workerId: string) => scan(workerId, false),
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
    if (open)
      void store.load(id)
  }))

  return {
    providers: () => store.providers(workerId()),
    loading: () => store.loading(workerId()),
    refresh: () => store.refresh(workerId()),
  }
}
