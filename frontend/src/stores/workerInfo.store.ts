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
import { createInflightCache } from '~/lib/inflightCache'
import { shallowEqual } from '~/lib/shallowEqual'
import { getWorkerInfo, setWorkerInfo } from '~/lib/workerInfoCache'

type InfoMap = Record<string, WorkerInfo>

export function createWorkerInfoStore() {
  const [infoMap, setInfoMap] = createSignal<InfoMap>({})
  const pending = createInflightCache<string, WorkerInfo | null>()

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
    return pending.run(workerId, async () => {
      try {
        const resp = await getWorkerSystemInfo(workerId)
        const info: WorkerInfo = {
          name: resp.name,
          os: resp.os,
          arch: resp.arch,
          homeDir: resp.homeDir,
          version: resp.version,
          commitHash: resp.commitHash,
          buildTime: resp.buildTime,
          updatedAt: Date.now(),
        }
        setWorkerInfo(workerId, info)
        setInfoMap((prev) => {
          const existing = prev[workerId]
          // updatedAt changes on every fetch, so normalize it before comparing.
          if (existing && shallowEqual({ ...existing, updatedAt: 0 }, { ...info, updatedAt: 0 }))
            return prev
          return { ...prev, [workerId]: info }
        })
        return info
      }
      catch {
        return null
      }
    })
  }

  /** Convenience: get homeDir for a worker (from cache), or empty string. */
  function getHomeDir(workerId: string): string {
    return workerInfo(workerId)?.homeDir ?? ''
  }

  /** Convenience: get the worker's reported OS (from cache), or undefined. */
  function getOs(workerId: string): string | undefined {
    return workerInfo(workerId)?.os
  }

  return {
    workerInfo,
    fetchWorkerInfo,
    getHomeDir,
    getOs,
  }
}
