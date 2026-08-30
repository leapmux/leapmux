/**
 * Final-close classification, shared by every long-lived WebSocket the app
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
 * long-lived subscription's reconnect): here only a hard final close stops
 * the retry loop and is surfaced to the caller.
 */
const FINAL_CLOSE_CODES = new Set<number>([
  1002, // protocol error
  1008, // policy violation -- the hub's auth expiry / revocation, or its per-user connection cap
])

/** Whether a close code means "stop retrying and tell the user". */
export function isFinalCloseCode(code: number): boolean {
  return FINAL_CLOSE_CODES.has(code)
}

/**
 * The hub's policy-violation close-reason TOKENS live in contracts/wire.json
 * (generated on both sides; the Go twin is channelwire.CloseReason*); fatalCloseMessage
 * imports them from ~/generated/contracts/wire.
 *
 * Branching on them matters: every other policy-violation close means
 * "re-authenticate", and too_many_connections means "close a tab" — opposite
 * advice, so a client that cannot tell them apart tells the user the wrong
 * thing. snapshot_to_large is final (a frame bigger than the pool's capacity
 * is refused at every occupancy); forbidden cannot be fixed by re-authenticating
 * or reloading; control_flood clears on reload because the new socket starts
 * with a full allowance.
 */

/** The close code and reason a final close carried, as both sockets report it. */
export interface FatalCloseInfo {
  code: number
  reason: string
}
