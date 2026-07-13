// The Go sidecar's channel relay is a process-wide singleton that
// OpenChannelRelay REUSES when it is still live, and CloseChannelRelay tears down
// whichever relay is currently installed -- it has no notion of which wrapper
// opened it. So only the most recent wrapper may close it: otherwise an earlier
// wrapper whose close raced its own open lands afterwards and tears down the
// relay a successor already adopted, leaving the successor silently wedged (the
// sidecar cancels the relay context before emitting, so no `channel:close` ever
// arrives and every send fails into `dispatchSend`'s catch).
//
// A wrapper claims ownership synchronously in its constructor, before any await, so
// a successor constructed at any point during a predecessor's async teardown has
// already taken the claim by the time that teardown runs -- single-threaded JS
// makes the check deterministic rather than a narrowed race. The claim is an id
// rather than the wrapper itself, so a superseded wrapper is not retained.
//
// The id sequence is a persisted, clock-seeded high-water mark rather than a
// counter starting at 0 -- the sidecar's relay owner outlives a webview reload,
// and a restarted counter would have the fresh page's open (the one that must
// win) refused by the sidecar's owner fence (`current.owner > relayID`) as
// superseded, wedging the channel until an app restart. The seeding rule lives
// in createPersistedSeq, shared with the org-events relay's id sequence.

import { KEY_CHANNEL_RELAY_SEQ } from './browserStorage'
import { createPersistedSeq } from './persistedSeq'

const NO_RELAY_OWNER = 0

const nextRelayWrapperId = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
let latestRelayWrapperId = NO_RELAY_OWNER

/**
 * relayClaim arbitrates ownership of the sidecar's singleton channel relay between
 * the wrappers that come and go over it. Exactly one wrapper -- the most recent one
 * to claim -- may tear the relay down, unless nobody owns it at all.
 */
export const relayClaim = {
  /** Takes ownership for a new wrapper, displacing the current claimant, and names it. */
  claim(): number {
    latestRelayWrapperId = nextRelayWrapperId()
    return latestRelayWrapperId
  },

  /**
   * Whether `id` may tear the relay down, surrendering the claim if so.
   *
   * True while `id` is the claimant, OR while there is no claimant at all: a successor
   * whose own open failed abandons its claim (see abandon), and the relay must not be
   * stranded just because the wrapper that displaced the caller never got one.
   *
   * Ownership is NOT surrendered by a superseded caller, only by a releasing one: a
   * close() that races its own open must still be able to reap the relay the in-flight
   * open goes on to install, which is what the wrapper's post-open guard does.
   */
  releaseIfClaimable(id: number): boolean {
    if (latestRelayWrapperId !== id && latestRelayWrapperId !== NO_RELAY_OWNER)
      return false
    latestRelayWrapperId = NO_RELAY_OWNER
    return true
  },

  /**
   * Drops `id`'s claim when its open never landed, marking the relay UNOWNED so a
   * predecessor still driving a live one can reap it.
   *
   * It marks unowned rather than restoring the displaced id, because that id may
   * itself be a wrapper that never got a relay: with A live and B then C both
   * failing, restoring C's predecessor would hand the claim to the dead B and leave
   * A unable to close -- the same strand this exists to prevent. "Unowned" is the
   * only honest answer, and releaseIfClaimable treats it as claimable.
   *
   * Only the current claimant may abandon; once a further successor has claimed, this
   * is a no-op and the relay is that successor's to reap.
   */
  abandon(id: number): void {
    if (latestRelayWrapperId === id)
      latestRelayWrapperId = NO_RELAY_OWNER
  },
}
