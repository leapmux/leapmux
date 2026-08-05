/**
 * Terminal-close classification, shared by every long-lived WebSocket the app
 * keeps open against the Hub.
 *
 * This is the browser counterpart of the Hub's `channelwire.isRecoverableCloseCode`,
 * and it lives in one place for the same reason that one does: BOTH long-lived
 * sockets -- `/ws/userevents` and `/ws/channel` -- can be closed by the same Hub
 * code path (`webSocketAuthLease.bind`) with the same code and reason, so a
 * classifier that only one of them consulted would let the other retry forever
 * against a refusal that can never change its mind.
 */

/**
 * Close codes on which auto-reconnect is futile: a genuine authorization or
 * protocol failure where retrying in a loop cannot succeed. Every OTHER close --
 * clean (1000/1001), transient (1012/1013), or an abnormal transport drop
 * (1006, no close frame) -- is a reconnect signal, so a network blip never kills
 * the subscription. This is intentionally broader than the backend's
 * `channelwire.isRecoverableCloseCode` (which drives the CLI's clean-exit, not a
 * long-lived subscription's reconnect): here only a hard terminal close stops
 * the retry loop and is surfaced to the caller.
 */
const TERMINAL_CLOSE_CODES = new Set<number>([
  1002, // protocol error
  1008, // policy violation -- the hub's auth expiry / revocation, or its per-user connection cap
])

/** Whether a close code means "stop retrying and tell the user". */
export function isTerminalCloseCode(code: number): boolean {
  return TERMINAL_CLOSE_CODES.has(code)
}

/**
 * WebSocket close reason the hub sends, alongside a policy-violation status,
 * when a user already holds as many long-lived connections as
 * `max_connections_per_user` allows.
 *
 * Branching on it matters: every other policy-violation close means
 * "re-authenticate", and this one means "close a tab" — opposite advice, so a
 * client that cannot tell them apart tells the user the wrong thing. The Go side
 * keeps it in `channelwire.CloseReasonTooManyConnections` and both pin it to the
 * cross-language fixture (testdata/channelwire_limits.json).
 */
export const CLOSE_REASON_TOO_MANY_CONNECTIONS = 'too_many_connections'

/** The close code and reason a terminal close carried, as both sockets report it. */
export interface FatalCloseInfo {
  code: number
  reason: string
}

/**
 * Close reason the hub sends when a subscriber's opening snapshot is larger
 * than the ENTIRE user-events queue budget.
 *
 * Distinct from a transient shortage, and the distinction is why it is a
 * terminal close: a frame bigger than the pool's capacity is refused at every
 * occupancy, so "retry later" produces a client that rebuilds the same
 * oversized snapshot forever while the user watches an app that never loads.
 * Only an operator can fix it, by raising `userevents_queue_memory_budget`.
 */
export const CLOSE_REASON_SNAPSHOT_TOO_LARGE = 'snapshot_too_large'

/**
 * Close reason the hub sends when an ACL check refuses the subscription.
 *
 * Its own token because the advice differs from every other policy violation on
 * this socket: re-authenticating cannot grant a permission, and reloading asks
 * for the same thing again. Without it this arrived as an unrecognised 1008 and
 * the user was told to reload — the one action guaranteed not to help.
 */
export const CLOSE_REASON_FORBIDDEN = 'forbidden'

/**
 * Close reason the hub sends when this client floods control frames past its
 * allowance.
 *
 * Nothing about the account, the workspace or the credential is wrong, so copy
 * aimed at any of those sends the user looking in the wrong place. A reload does
 * clear it, because the new socket starts with a full allowance.
 */
export const CLOSE_REASON_CONTROL_FLOOD = 'too_many_control_frames'
