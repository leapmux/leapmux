import type { ChannelManager } from '~/lib/channel'
import { createSignal, onCleanup } from 'solid-js'

export type ChannelStatus = 'connected' | 'warning' | 'disconnected'

interface WarningState {
  lastErrorAt: number
}

const WARNING_EXPIRY_MS = 10_000

export function createWorkerChannelStatusStore(cm: ChannelManager) {
  const [statusMap, setStatusMap] = createSignal<Record<string, ChannelStatus>>({})
  const warnings = new Map<string, WarningState>()

  function refreshAll(workerIds: string[]) {
    setStatusMap(() => {
      const map: Record<string, ChannelStatus> = {}
      for (const id of workerIds) {
        if (warnings.has(id)) {
          map[id] = 'warning'
        }
        else {
          map[id] = cm.hasOpenChannel(id) ? 'connected' : 'disconnected'
        }
      }
      return map
    })
  }

  let trackedWorkerIds: string[] = []

  function setWorkerIds(ids: string[]) {
    trackedWorkerIds = ids
    refreshAll(ids)
  }

  const unsubState = cm.onStateChange(() => {
    refreshAll(trackedWorkerIds)
  })

  const unsubError = cm.onChannelError((workerId) => {
    warnings.set(workerId, { lastErrorAt: Date.now() })
    refreshAll(trackedWorkerIds)
  })

  const interval = setInterval(() => {
    let changed = false
    const now = Date.now()
    for (const [workerId, state] of warnings) {
      if (now - state.lastErrorAt >= WARNING_EXPIRY_MS) {
        warnings.delete(workerId)
        changed = true
      }
    }
    if (changed) {
      refreshAll(trackedWorkerIds)
    }
  }, WARNING_EXPIRY_MS)

  onCleanup(() => {
    unsubState()
    unsubError()
    clearInterval(interval)
  })

  function getStatus(workerId: string): ChannelStatus {
    return statusMap()[workerId] ?? 'disconnected'
  }

  return {
    getStatus,
    setWorkerIds,
  }
}
