import { createSignal } from 'solid-js'
import { KEY_CLIENT_ID, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { randomUUID } from '~/lib/idGenerator'
import { createLogger } from '~/lib/logger'

const log = createLogger('clientIdentity')

// ---------------------------------------------------------------------------
// Per-tab CRDT client identity, with a liveness handshake
//
// The client id is the HLC author, the op_id salt, and — since the client
// checkpoint landed — the second half of the `(userId, clientId)` key that owns
// a persisted checkpoint + op-log. It lives in sessionStorage so it SURVIVES A
// REFRESH, which is what makes cross-refresh delta-resume work: reload the same
// tab, find your own checkpoint, resume from it.
//
// THE PROBLEM sessionStorage alone does not solve: browsers COPY sessionStorage
// into a duplicated tab. "Duplicate tab" (Chrome/Edge), "Reopen closed tab", and
// Firefox/Safari's equivalents all clone it, so two LIVE tabs end up holding the
// same id. They then own the same checkpoint row, and
// `writeCheckpointAndTruncateOpLog` deletes the whole owner range — so each
// tab's rewrite truncates the other's op-log segments. The next reload replays
// state@T1 plus only the post-truncate tail, with T1..T2 gone, and the
// `batchEnd` frames in that tail advance the resume cursor straight past the
// hole. The hub ships only what is strictly after the cursor, so those ops are
// never re-sent: silent, permanent divergence. This handshake is what narrows
// that window; it does not close it (see below), which is why
// checkpointStore's header calls the per-tab key "unlikely" rather than
// "unrepresentable".
//
// THE HANDSHAKE. Every tab announces `claim` for the id it holds, tagged with a
// per-PAGE-LOAD instance token (which sessionStorage duplication cannot clone —
// it is minted in memory, after the copy). A tab that hears a `claim` for the id
// it is currently using, from a different instance, answers `held` ADDRESSED TO
// THAT CLAIMANT. A tab that hears a `held` addressed to itself knows it is the
// duplicate and re-mints.
//
// The incumbent wins because it is the one already listening: the duplicate
// necessarily loads later, so its `claim` reaches a live incumbent while the
// incumbent's original `claim` predates the duplicate entirely — and because
// only the claimant acts on the answer. Addressing is load-bearing, not
// tidiness: with a broadcast `held`, a THIRD tab holding the same id answers a
// duplicate's claim and the incumbent reads that answer as a verdict on itself,
// so duplicating a tab twice in quick succession re-mints the ORIGINAL. See
// ClaimMessage.replyTo.
//
// Reassignment is REACTIVE, not fatal: `clientId` is a signal, the runtime's
// hydration effect reads it, and the duplicate simply re-hydrates under its
// fresh id — a checkpoint miss, so it cold-starts with one full snapshot. That
// is the correct outcome for a tab that has applied nothing of its own, and it
// costs the duplicate a snapshot rather than costing BOTH tabs their history.
//
// Degrades cleanly: without BroadcastChannel (older Safari, some embedded
// webviews) the id is used as-is, which is exactly today's behavior.
// ---------------------------------------------------------------------------

const CHANNEL_NAME = 'leapmux:crdt-client-id'

interface ClaimMessage {
  type: 'claim' | 'held'
  /** The id being claimed or held. */
  clientId: string
  /** Per-page-load token. Distinguishes a duplicated tab from its source. */
  instance: string
  /**
   * On a `held`, the `instance` of the claimant it answers. ADDRESSED, not
   * broadcast: only that tab may treat it as evidence against its own id.
   *
   * Without it a `held` is heard by every tab holding the id, and with THREE
   * such tabs the incumbent loses. A duplicate B claims X; both the incumbent A
   * and a second duplicate C still hold X, so both answer `held(X)`; A then
   * sees C's answer, reads it as "someone else owns my id", and re-mints
   * itself. The header's argument that "the incumbent always wins because it is
   * the one already listening" holds only while at most one other tab holds the
   * id -- it is an argument about who ANSWERS, and says nothing about who the
   * answer is FOR.
   */
  replyTo?: string
}

export interface ClientIdentity {
  /**
   * The id this tab owns. A SIGNAL because a duplicate tab re-mints after the
   * handshake resolves; readers that key persistent state on it (the checkpoint
   * owner, the HLC author) must re-run when it changes.
   */
  clientId: () => string
  /** Release the channel. Idempotent. */
  dispose: () => void
}

function mintClientId(): string {
  const fresh = `c-${randomUUID()}`
  sessionStorageSet(KEY_CLIENT_ID, fresh)
  return fresh
}

/**
 * Resolve this tab's client id and defend it against a duplicated tab.
 *
 * Returns immediately with the stored (or freshly minted) id — the handshake is
 * asynchronous and only ever *narrows* to a new id, so no caller has to wait.
 */
export function createClientIdentity(): ClientIdentity {
  const [clientId, setClientId] = createSignal(sessionStorageGet<string>(KEY_CLIENT_ID) || mintClientId())

  if (typeof BroadcastChannel === 'undefined')
    return { clientId, dispose: () => {} }

  // Minted in memory AFTER any sessionStorage copy, so a duplicated tab and its
  // source always disagree here even though they agree on clientId.
  const instance = randomUUID()
  let channel: BroadcastChannel | null
  try {
    channel = new BroadcastChannel(CHANNEL_NAME)
  }
  catch {
    // Some webviews expose the constructor but refuse to construct it.
    return { clientId, dispose: () => {} }
  }

  const post = (type: ClaimMessage['type'], replyTo?: string): void => {
    try {
      channel?.postMessage({ type, clientId: clientId(), instance, replyTo } satisfies ClaimMessage)
    }
    catch {
      // A closed channel or a structured-clone failure: the id stays as-is,
      // which is the pre-handshake behavior.
    }
  }

  channel.onmessage = (event: MessageEvent<ClaimMessage>) => {
    const msg = event.data
    // Ignore our own echo.
    if (!msg || msg.instance === instance)
      return
    // Every message is about one specific id; ignore it unless it is ours.
    if (msg.clientId !== clientId())
      return
    if (msg.type === 'claim') {
      // We were here first and are still using this id. Say so, addressed to
      // the claimant so no OTHER holder mistakes this for a verdict on itself.
      post('held', msg.instance)
      return
    }
    // msg.type === 'held'. Only the tab that asked may act on the answer.
    // Ignoring the rest is what keeps the incumbent's id stable when two or
    // more duplicates race: each duplicate re-mints in response to its own
    // claim, and the tab that claimed X before any of them existed never sees
    // an answer addressed to itself.
    if (msg.replyTo !== instance)
      return
    // Someone else already owns this id, so we are the duplicate. Re-mint and
    // claim the new one.
    const previous = clientId()
    const replacement = mintClientId()
    setClientId(replacement)
    log.warn('client id collided with a live tab (duplicated tab?); re-minted', { previous, replacement })
    post('claim')
  }

  post('claim')

  return {
    clientId,
    dispose: () => {
      channel?.close()
      channel = null
    },
  }
}
