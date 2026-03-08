/**
 * Reactive store for worker system info fetched via E2EE channel.
 *
 * - On first access, hydrates from localStorage cache.
 * - fetchWorkerInfo() queries the worker via E2EE and updates both
 *   the reactive signal and the localStorage cache.
 * - Provides getHomeDir() convenience accessor for tilde path display.
 */

import type { WorkerInfo } from '~/lib/workerInfoCache'
import { createSignal } from 'solid-js'
import { getWorkerSystemInfo } from '~/api/workerRpc'
import { getWorkerInfo, setWorkerInfo } from '~/lib/workerInfoCache'

type InfoMap = Record<string, WorkerInfo>

export function createWorkerInfoStore() {
  const [infoMap, setInfoMap] = createSignal<InfoMap>({})
  // Track in-flight fetches to avoid duplicate requests.
  const pending = new Map<string, Promise<WorkerInfo | null>>()

  /** Get cached info for a worker (reactive). Returns null if not yet fetched. */
  function workerInfo(workerId: string): WorkerInfo | null {
    const map = infoMap()
    if (map[workerId])
      return map[workerId]

    // Hydrate from localStorage on first access.
    const cached = getWorkerInfo(workerId)
    if (cached) {
      setInfoMap(prev => ({ ...prev, [workerId]: cached }))
      return cached
    }
    return null
  }

  /** Fetch system info from an online worker via E2EE and cache it. */
  async function fetchWorkerInfo(workerId: string): Promise<WorkerInfo | null> {
    // Deduplicate concurrent fetches for the same worker.
    const existing = pending.get(workerId)
    if (existing)
      return existing

    const promise = (async () => {
      try {
        const resp = await getWorkerSystemInfo(workerId)
        const info: WorkerInfo = {
          name: resp.name,
          os: resp.os,
          arch: resp.arch,
          homeDir: resp.homeDir,
          version: resp.version,
          updatedAt: Date.now(),
        }
        setWorkerInfo(workerId, info)
        setInfoMap(prev => ({ ...prev, [workerId]: info }))
        return info
      }
      catch {
        return null
      }
      finally {
        pending.delete(workerId)
      }
    })()

    pending.set(workerId, promise)
    return promise
  }

  /** Convenience: get homeDir for a worker (from cache), or empty string. */
  function getHomeDir(workerId: string): string {
    return workerInfo(workerId)?.homeDir ?? ''
  }

  return {
    workerInfo,
    fetchWorkerInfo,
    getHomeDir,
  }
}
