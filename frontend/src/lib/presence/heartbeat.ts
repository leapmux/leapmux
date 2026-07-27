import { onCleanup } from 'solid-js'
import { userCRDTClient } from '~/api/clients'
import { monotonicNow } from '~/lib/monotonicNow'

/**
 * HEARTBEAT_THROTTLE_MS bounds how often input events translate into
 * UpdatePresence RPCs. The hub keeps a client's presence entry alive
 * for the lifetime of the `/ws/userevents` subscription (with a short
 * grace period on disconnect), so the throttle exists purely to avoid
 * flooding the wire while a user is typing — not to satisfy an
 * inactivity window.
 */
const HEARTBEAT_THROTTLE_MS = 5_000

interface HeartbeatOpts {
  /**
   * Reactive accessor for the workspace currently in view; "" or null
   * disables heartbeats. The hub tracks presence per-workspace, so
   * this MUST be the active workspace.
   */
  workspaceId: () => string | null
  /**
   * Optional override for sending the UpdatePresence RPC; injected
   * by tests so they don't need a transport stack.
   */
  sender?: (workspaceId: string) => Promise<void> | void
}

/**
 * Handle returned by `mountPresenceHeartbeat`. Callers invoke
 * `pingNow()` on `/ws/userevents` (re)connect — the hub-side presence
 * broadcast only reaches a client that's already subscribed, so a
 * heartbeat sent before the WS handshake completes never returns its
 * own `PresenceUpdate` event and the client never learns it's active.
 * Stream mount / reconnect is the moment to claim presence.
 */
export interface HeartbeatHandle {
  /**
   * Force an immediate heartbeat, bypassing the input-throttle. Used
   * by the WS subscription effect on bootstrap so the hub stamps
   * `received_at` AFTER this client is subscribed to receive the
   * resulting broadcast.
   */
  pingNow: () => void
  /** Detach listeners. */
  stop: () => void
}

/**
 * mountPresenceHeartbeat wires document-level input listeners
 * (keydown / pointerdown / wheel) and a visibility-change listener.
 * Returns a handle exposing `pingNow()` and a `stop()` cleanup.
 *
 * No inactivity keepalive is needed: the hub holds the presence entry
 * for the lifetime of the `/ws/userevents` subscription (plus a short
 * grace window on disconnect), so an idle-but-connected tab remains
 * the active client without sending periodic pings.
 *
 * The reactive plumbing is deliberately Solid-flavored: callers wrap
 * `stop` in `onCleanup` (default behavior when called inside a Solid
 * root). When called outside a Solid root, the caller is responsible
 * for invoking `stop` themselves.
 *
 * `pingNow` should be called whenever a fresh `/ws/userevents`
 * subscription bootstraps (initial connect, reconnect) — heartbeats
 * sent before the subscription is live race the hub's broadcast and
 * the client misses its own `PresenceUpdate`.
 */
export function mountPresenceHeartbeat(opts: HeartbeatOpts): HeartbeatHandle {
  // undefined = never sent. A numeric zero would throttle the first input
  // for ~5s after page load because performance.now() starts near 0.
  let lastSent: number | undefined

  const send = (immediate = false) => {
    const workspaceId = opts.workspaceId() ?? ''
    if (!workspaceId)
      return
    const now = monotonicNow()
    if (!immediate && lastSent !== undefined && now - lastSent < HEARTBEAT_THROTTLE_MS)
      return
    lastSent = now
    const sender = opts.sender ?? defaultSender
    void Promise.resolve(sender(workspaceId)).catch(() => {})
  }

  const onInput = () => send(false)
  const onVisibilityChange = () => {
    if (document.visibilityState === 'visible')
      send(true)
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('keydown', onInput, { passive: true })
    document.addEventListener('pointerdown', onInput, { passive: true })
    document.addEventListener('wheel', onInput, { passive: true })
    document.addEventListener('visibilitychange', onVisibilityChange)
  }

  // Module-mount fire is intentionally NOT done here — the hub
  // broadcasts `PresenceUpdate` to current subscribers, and a
  // heartbeat sent before `/ws/userevents` is connected never reaches
  // this client. `pingNow()` is the stream-mount-driven entry point
  // the caller wires once bootstrap completes.

  const stop = () => {
    if (typeof document !== 'undefined') {
      document.removeEventListener('keydown', onInput)
      document.removeEventListener('pointerdown', onInput)
      document.removeEventListener('wheel', onInput)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }

  // When invoked inside a Solid root, register cleanup automatically.
  // Outside one this is a no-op and callers must call the returned fn.
  try {
    onCleanup(stop)
  }
  catch {
    // not in a reactive scope
  }
  return {
    pingNow: () => send(true),
    stop,
  }
}

async function defaultSender(workspaceId: string): Promise<void> {
  await userCRDTClient.updatePresence({ workspaceId })
}
