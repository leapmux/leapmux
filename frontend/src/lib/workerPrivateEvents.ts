// Per-worker subscriber for the worker's E2EE-only
// `WatchWorkerPrivateEvents` stream. Decoded events — `TabRenamed`,
// `TabPayloadRegistered`, `TabPayloadRevoked` — are surfaced to the
// caller; reconnect happens transparently. The worker emits a one-shot
// bootstrap reply at subscribe time (one `TabPayloadRegistered` per
// `worker_tab_payloads` row the caller owns) so a late-joining client
// receives the full payload cache before any live events.

import type { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { TabPayloadView } from '~/lib/tabPayload'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { channelManager } from '~/api/workerRpc'
import {
  WatchWorkerPrivateEventsRequestSchema,
  WorkerPrivateEventSchema,
} from '~/generated/proto/leapmux/v1/worker_private_pb'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'
import { tabPayloadView } from '~/lib/tabPayload'

const log = createLogger('workerPrivateEvents')

// One stream per call, so a single fixed backoff key suffices.
const RECONNECT_KEY = 'reconnect'
const RECONNECT_INITIAL_MS = 250
const RECONNECT_MAX_MS = 8000

interface OpenStreamOpts {
  workerId: string
  onTabRenamed: (evt: { tabId: string, tabType: TabType, title: string, originClientId: string }) => void
  /**
   * Optional callback for `TabPayloadRegistered` events — fires both
   * during the bootstrap replay and on live updates. Idempotent on the
   * receiver side: it patches `tabMetadata`, which drops a write equal to
   * what is stored.
   *
   * `payload.workingDir` is the tab's git context as the WORKER resolved it
   * (see the RPC's proto doc), so a client that learns of the tab here groups
   * it exactly where the client that opened it did.
   */
  onTabPayloadRegistered?: (evt: { tabId: string, payload: TabPayloadView }) => void
  /**
   * Optional callback for `TabPayloadRevoked` events. Receiver drops
   * the (tab_id → path) entry from the local cache.
   */
  onTabPayloadRevoked?: (evt: { tabId: string }) => void
}

/**
 * Open a per-worker private-event subscription. The returned function
 * tears the subscription down when called; the implementation
 * reconnects with backoff on transport errors.
 */
export function openWorkerPrivateEventStream(opts: OpenStreamOpts): () => void {
  let stopped = false
  let currentClose: (() => void) | null = null
  // Resolver for a active stream wait, so teardown can wake the loop immediately
  // instead of waiting for a transport-level error that may never fire —
  // handle.cancel() detaches the listener synchronously, so onEnd/onError never
  // resolve the stream-wait Promise on a clean teardown with a live channel.
  let wakeStream: (() => void) | null = null
  // Resolver for a pending reconnect wait, so teardown can wake the loop
  // immediately instead of letting it sit out the full backoff delay.
  let wakeReconnect: (() => void) | null = null

  // Shared jittered backoff (matching useUserEvents / useWorkspaceConnection)
  // in place of the previous hand-rolled, jitter-free doubling closure.
  const reconnectBackoff = createExponentialBackoff<string>({
    initialMs: RECONNECT_INITIAL_MS,
    maxMs: RECONNECT_MAX_MS,
  })

  const start = async () => {
    // eslint-disable-next-line no-unmodified-loop-condition
    while (!stopped) {
      // A fresh (re)connect: reset the backoff streak once this attempt proves
      // healthy by delivering its first event (the worker's bootstrap replay or
      // a live update), mirroring useUserEvents' reset-on-bootstrap so a merely
      // opened-then-immediately-dropped stream keeps backing off.
      let healthy = false
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
        const req = create(WatchWorkerPrivateEventsRequestSchema, {})
        const payload = toBinary(WatchWorkerPrivateEventsRequestSchema, req)
        const handle = channelManager.stream(channelId, 'WatchWorkerPrivateEvents', payload)
        currentClose = () => handle.cancel()

        await new Promise<void>((resolve) => {
          wakeStream = resolve
          handle.onMessage((msg) => {
            if (!healthy) {
              healthy = true
              reconnectBackoff.reset(RECONNECT_KEY)
            }
            try {
              const evt = fromBinary(WorkerPrivateEventSchema, msg.payload)
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
                case 'tabPayloadRegistered': {
                  const r = evt.event.value
                  // A payload this client cannot read -- a tab kind a newer
                  // peer registered -- is dropped rather than half-applied:
                  // patching a partial shape would leave a tab claiming to be
                  // a FILE with no path.
                  const payload = tabPayloadView(r.payload)
                  if (payload)
                    opts.onTabPayloadRegistered?.({ tabId: r.tabId, payload })
                  break
                }
                case 'tabPayloadRevoked': {
                  const r = evt.event.value
                  opts.onTabPayloadRevoked?.({ tabId: r.tabId })
                  break
                }
              }
            }
            catch (err) {
              log.warn('failed to decode private event', { workerId: opts.workerId, err })
            }
          })
          handle.onEnd(() => resolve())
          handle.onError((err) => {
            log.debug('private event stream error', { workerId: opts.workerId, err })
            resolve()
          })
        })
      }
      catch (err) {
        log.debug('failed to open private event stream; will retry', { workerId: opts.workerId, err })
      }
      wakeStream = null
      currentClose = null
      if (stopped)
        return
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
    wakeStream?.()
    wakeStream = null
    wakeReconnect?.()
    wakeReconnect = null
    try {
      currentClose?.()
    }
    catch { /* ignore */ }
  }
}
