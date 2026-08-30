import type { FatalCloseInfo } from './wsCloseCodes'
import {
  CLOSE_REASON_CONTROL_FLOOD,
  CLOSE_REASON_FORBIDDEN,
  CLOSE_REASON_SNAPSHOT_TOO_LARGE,
  CLOSE_REASON_TOO_MANY_CONNECTIONS,
} from '~/generated/contracts/wire'
import { ChannelError } from './channelError'

/**
 * Copy for a long-lived hub stream that closed with a final code, keyed on
 * the hub's close reason.
 *
 * A pure function rather than an `if` inside a hook: the branches are then
 * testable without standing up the CRDT runtime, and the fallback stays a
 * fallback. Every final close used to produce one message — "reload the page"
 * — which is right for an expired credential and actively WRONG for the
 * connection cap, where a reload just produces another refused connection.
 *
 * It lives in `lib` rather than beside the toast because BOTH long-lived
 * sockets can be refused this way, and the channel relay -- which is `lib` and
 * cannot import from `components` -- surfaces the same message as the reason it
 * drains its channels with.
 */
export function fatalCloseMessage(info: FatalCloseInfo): string {
  if (info.reason === CLOSE_REASON_TOO_MANY_CONNECTIONS) {
    return 'LeapMux is open in too many places for your account. '
      + 'Close another tab, window, or CLI session, then reload this page.'
  }
  if (info.reason === CLOSE_REASON_SNAPSHOT_TOO_LARGE) {
    // Nothing the user can do, so do not imply otherwise: reloading and closing
    // tabs both leave the snapshot exactly as large as it was.
    return 'Your workspace is too large for this server\u2019s configured limit. '
      + 'Ask an administrator to raise the user-events queue memory budget.'
  }
  if (info.reason === CLOSE_REASON_FORBIDDEN) {
    // Not a credential problem, so do not send them to sign in again: the
    // account is fine and simply may not have this.
    return 'You do not have access to this workspace. '
      + 'Ask an administrator to grant it, then reload this page.'
  }
  if (info.reason === CLOSE_REASON_CONTROL_FLOOD) {
    // The one final reason a plain reload really does fix, because the new
    // socket starts with a full allowance.
    return 'This tab was disconnected for sending too many keepalive messages. '
      + 'Reload the page to reconnect.'
  }
  return 'Live updates disconnected. Reload the page to reconnect.'
}

/**
 * The channel-transport error a final close produces, carrying both the copy
 * and the `fatal` marker.
 *
 * One factory so every site that fails a caller because of a refused connection
 * says the same thing and is recognisable as the same thing -- the marker is
 * what lets a caller tell "the hub refused us" from "the worker went away",
 * which are opposite diagnoses for the user.
 */
export function fatalCloseError(info: FatalCloseInfo): ChannelError {
  return new ChannelError('transport', fatalCloseMessage(info), { fatal: true })
}
