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
import { Code } from '@connectrpc/connect'
import { channelManager } from '~/api/workerRpc'
import { ensureWorkspaceAccess } from '~/api/workspaceAccess'
import {
  WatchWorkspacePrivateEventsRequestSchema,
  WorkspacePrivateEventSchema,
} from '~/generated/leapmux/v1/workspace_private_pb'
import { ChannelError } from '~/lib/channelError'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'

const log = createLogger('workspacePrivateEvents')

// One stream per call, so a single fixed backoff key suffices.
const RECONNECT_KEY = 'reconnect'
const RECONNECT_INITIAL_MS = 250
const RECONNECT_MAX_MS = 8000

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
  // Resolver for a pending reconnect wait, so teardown can wake the loop
  // immediately instead of letting it sit out the full backoff delay.
  let wakeReconnect: (() => void) | null = null

  // Shared jittered backoff (matching useUserEvents / useWorkspaceConnection)
  // in place of the previous hand-rolled, jitter-free doubling closure.
  const reconnectBackoff = createExponentialBackoff<string>({
    initialMs: RECONNECT_INITIAL_MS,
    maxMs: RECONNECT_MAX_MS,
  })

  /**
   * The worker refuses the whole stream when this channel has not been told
   * about the workspace (`registerWorkspaceGatedStream` -> PermissionDenied).
   * That is not a transient fault and reconnecting cannot clear it: the
   * accessible set grows only when someone calls PrepareWorkspaceAccess, so a
   * workspace created outside this page -- by the `leapmux remote` CLI, by
   * another session -- would otherwise have this loop reconnect into the same
   * refusal every 8s for the life of the page.
   *
   * `ChannelError.code` carries the worker's raw gRPC status (`sendStreamError`
   * writes `int32(codes.PermissionDenied)` into `InnerStreamMessage.error_code`).
   * Connect's `Code` enum shares gRPC's numbering, so it names the constant
   * without a mapping table.
   */
  const deniedForWorkspace = (err: Error): boolean =>
    err instanceof ChannelError && err.code === Code.PermissionDenied

  /** Announce the workspace; true when this call is what changed the answer. */
  const announceWorkspace = async (): Promise<boolean> => {
    try {
      return await ensureWorkspaceAccess(opts.workerId, opts.workspaceId)
    }
    catch (err) {
      // Falling through to the backoff also retries the announcement, since a
      // failure is not remembered.
      log.debug('failed to announce workspace access', { workerId: opts.workerId, workspaceId: opts.workspaceId, err })
      return false
    }
  }

  const start = async () => {
    // eslint-disable-next-line no-unmodified-loop-condition
    while (!stopped) {
      // A fresh (re)connect: reset the backoff streak once this attempt proves
      // healthy by delivering its first event (the worker's bootstrap replay or
      // a live update), mirroring useUserEvents' reset-on-bootstrap so a merely
      // opened-then-immediately-dropped stream keeps backing off.
      let healthy = false
      let denied = false
      try {
        const channelId = await channelManager.getOrOpenChannel(opts.workerId)
        // Teardown may have run while we were awaiting the (genuinely async,
        // E2EE) channel open. At that instant currentClose was still null, so
        // the returned cleanup closed nothing -- bail before registering the
        // stream listener, or it would stay subscribed and keep firing the
        // opts.on* callbacks into a torn-down caller (mirrors useUserEvents'
        // attemptDisposed guard).
        if (stopped)
          return
        const req = create(WatchWorkspacePrivateEventsRequestSchema, { workspaceId: opts.workspaceId })
        const payload = toBinary(WatchWorkspacePrivateEventsRequestSchema, req)
        const handle = channelManager.stream(channelId, 'WatchWorkspacePrivateEvents', payload)
        // LEAK: `removeStreamListener` unregisters the LOCAL listener only —
        // it sends no cancel frame, and `ChannelManager.stream` exposes none.
        // The worker's `SnapshotAndSubscribe` loop exits only on `ctx.Done()`
        // (a background ctx) or a send error, so its goroutine and buffered
        // channel stay registered; a re-opened (workspace, worker) pair then
        // adds a SECOND subscriber and every event is pushed twice.
        // Tracked: https://github.com/leapmux/leapmux/issues/337
        currentClose = () => channelManager.removeStreamListener(channelId, handle.requestId)

        await new Promise<void>((resolve) => {
          handle.onMessage((msg) => {
            if (!healthy) {
              healthy = true
              reconnectBackoff.reset(RECONNECT_KEY)
            }
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
            denied = deniedForWorkspace(err)
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
      // Repair rather than re-dial: announce the workspace on this worker's
      // channels and reconnect at once. `ensureWorkspaceAccess` answers false
      // once the pair has already been announced, so a denial that survives the
      // announcement falls through to the backoff instead of spinning here.
      // Teardown during the announcement is caught by the loop condition.
      if (denied && await announceWorkspace())
        continue
      // Wait out the jittered backoff before retrying; teardown resolves this
      // early via wakeReconnect so a stopped stream doesn't linger.
      await new Promise<void>((resolve) => {
        wakeReconnect = resolve
        reconnectBackoff.schedule(RECONNECT_KEY, resolve)
      })
      wakeReconnect = null
    }
  }

  start().catch(() => {})

  return () => {
    stopped = true
    reconnectBackoff.cancelAll()
    wakeReconnect?.()
    wakeReconnect = null
    try {
      currentClose?.()
    }
    catch { /* ignore */ }
  }
}
