// Per-worker subscriber for the worker's E2EE-only
// `WatchWorkspacePrivateEvents` stream. Decoded events — `TabRenamed`,
// `FileTabPathRegistered`, `FileTabPathRevoked` — are surfaced to the
// caller; reconnect happens transparently. The worker emits a one-shot
// bootstrap reply at subscribe time (one `FileTabPathRegistered` per
// existing `worker_file_tabs` row in the requested workspace) so a
// late-joining client receives the full path cache before any live
// events.

import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { channelManager } from '~/api/workerRpc'
import {
  WatchWorkspacePrivateEventsRequestSchema,
  WorkspacePrivateEventSchema,
} from '~/generated/leapmux/v1/workspace_private_pb'
import { createLogger } from '~/lib/logger'
import { sleep } from '~/lib/sleep'

const log = createLogger('workspacePrivateEvents')

interface OpenStreamOpts {
  workspaceId: string
  workerId: string
  onTabRenamed: (evt: { tabId: string, tabType: TabType, title: string, originClientId: string }) => void
  /**
   * Optional callback for `FileTabPathRegistered` events — fires both
   * during the bootstrap replay and on live updates. Idempotent on the
   * receiver side (the `fileTabPaths` store dedupes by (tab_id, path)).
   */
  onFileTabPathRegistered?: (evt: { tabId: string, workspaceId: string, filePath: string }) => void
  /**
   * Optional callback for `FileTabPathRevoked` events. Receiver drops
   * the (tab_id → path) entry from the local cache.
   */
  onFileTabPathRevoked?: (evt: { tabId: string }) => void
}

/**
 * Open a per-worker private-event subscription. The returned function
 * tears the subscription down when called; the implementation
 * reconnects with backoff on transport errors.
 */
export function openWorkerPrivateEventStream(opts: OpenStreamOpts): () => void {
  let stopped = false
  let currentClose: (() => void) | null = null

  const reconnectBackoff = (() => {
    let ms = 250
    return () => {
      const v = ms
      ms = Math.min(ms * 2, 8000)
      return v
    }
  })()

  const start = async () => {
    // eslint-disable-next-line no-unmodified-loop-condition
    while (!stopped) {
      try {
        const channelId = await channelManager.getOrOpenChannel(opts.workerId)
        const req = create(WatchWorkspacePrivateEventsRequestSchema, { workspaceId: opts.workspaceId })
        const payload = toBinary(WatchWorkspacePrivateEventsRequestSchema, req)
        const handle = channelManager.stream(channelId, 'WatchWorkspacePrivateEvents', payload)
        currentClose = () => channelManager.removeStreamListener(channelId, handle.requestId)

        await new Promise<void>((resolve) => {
          handle.onMessage((msg) => {
            try {
              const evt = fromBinary(WorkspacePrivateEventSchema, msg.payload)
              switch (evt.event?.case) {
                case 'tabRenamed': {
                  const r = evt.event.value
                  opts.onTabRenamed({
                    tabId: r.tabId,
                    tabType: r.tabType,
                    title: r.title,
                    originClientId: r.originClientId,
                  })
                  break
                }
                case 'fileTabPathRegistered': {
                  const r = evt.event.value
                  opts.onFileTabPathRegistered?.({
                    tabId: r.tabId,
                    workspaceId: r.workspaceId,
                    filePath: r.filePath,
                  })
                  break
                }
                case 'fileTabPathRevoked': {
                  const r = evt.event.value
                  opts.onFileTabPathRevoked?.({ tabId: r.tabId })
                  break
                }
              }
            }
            catch (err) {
              log.warn('failed to decode private event', { workerId: opts.workerId, workspaceId: opts.workspaceId, err })
            }
          })
          handle.onEnd(() => resolve())
          handle.onError((err) => {
            log.debug('private event stream error', { workerId: opts.workerId, workspaceId: opts.workspaceId, err })
            resolve()
          })
        })
      }
      catch (err) {
        log.debug('failed to open private event stream; will retry', { workerId: opts.workerId, workspaceId: opts.workspaceId, err })
      }
      currentClose = null
      if (stopped)
        return
      await sleep(reconnectBackoff())
    }
  }

  start().catch(() => {})

  return () => {
    stopped = true
    try {
      currentClose?.()
    }
    catch { /* ignore */ }
  }
}
