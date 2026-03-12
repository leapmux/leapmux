import type { ChannelManager } from '~/lib/channel'
import { createSignal, onCleanup } from 'solid-js'

export type ChannelStatus = 'connected' | 'disconnected'

export function createWorkerChannelStatusStore(cm: ChannelManager) {
  // Version counter — bumped on channel state changes to trigger
  // SolidJS reactivity in getStatus() callers.
  const [version, setVersion] = createSignal(0)

  const unsubState = cm.onStateChange(() => {
    setVersion(v => v + 1)
  })

  onCleanup(() => {
    unsubState()
  })

  function getStatus(workerId: string): ChannelStatus {
    version() // subscribe to changes
    return cm.hasOpenChannel(workerId) ? 'connected' : 'disconnected'
  }

  return {
    getStatus,
  }
}
