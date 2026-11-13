import type { Accessor } from 'solid-js'
import { createMemo } from 'solid-js'
import { shallowEqualSets } from '~/lib/shallowEqual'

// ---------------------------------------------------------------------------
// Ordered tail reveal
//
// Newly appended messages mount hidden: a loading skeleton paints each reserved
// slot until the row has loaded, then the real row fades in. The caller decides
// what "loading" means (see `isLoading` below). Every row reveals once it ITSELF
// has loaded, so when several messages are appended at once the one that loads
// first pops in first -- making a later message appear ahead of an earlier one
// that is still a skeleton.
//
// This gate holds a loaded tail row hidden until every EARLIER still-loading
// row in the same append cohort has revealed, so a burst of appended messages
// always reveals in document order (never a later one first), even if the later
// one finishes loading first.
//
// Scope: only the tail-anchored cohort of not-yet-revealed rows is ordered.
// Rows already shown to the user are tracked in a sticky `released` set and are
// never re-hidden -- so scrolling back through already-loaded history (where a
// row entering at the TOP is briefly loading) can't flicker the already-visible
// rows below it, and reveal order there (which doesn't matter) is left untouched.
// ---------------------------------------------------------------------------

/**
 * A reactive set of LOADED rows to keep hidden purely to preserve in-order
 * reveal of an append burst: a loaded row is held while any earlier row in its
 * tail-anchored cohort is still loading. Empty in the steady state (nothing
 * loading) and while scrolling already-loaded history.
 *
 * @param orderedIds the visible rows' ids in document order (the rendered slice).
 * @param isLoading  whether a row is still loading, and so hidden on its own
 *                   account. MUST read reactively -- the gate re-runs when a
 *                   row's loading state flips. ChatView counts three cases:
 *                   - A row that awaits its own measurement.
 *                   - A row whose asynchronous body (Markdown, an edit diff)
 *                     is still pending, even when its height is known.
 *                   - A row that has not mounted yet. The row <For> mounts it
 *                     later in the same update, but until then the gate must
 *                     hold every later row behind it, because a release is
 *                     permanent.
 *
 * Call within a reactive owner: it allocates one memo and keeps a sticky
 * `released` set across runs.
 */
export function createOrderedTailReveal(
  orderedIds: Accessor<readonly string[]>,
  isLoading: (id: string) => boolean,
): Accessor<ReadonlySet<string>> {
  // Ids the gate has already released (shown). Sticky across runs so a row, once
  // revealed, is never re-hidden: an earlier row re-entering `isLoading` (a
  // height-key churn) can't flicker an already-visible later row, and the
  // ordering stays anchored at the append tail instead of leaking upward into
  // scrolled-in history.
  const released = new Set<string>()
  return createMemo<ReadonlySet<string>>(
    () => {
      const ids = orderedIds()
      const present = new Set(ids)
      // Bound the sticky set to the live window: a row scrolled away forgets its
      // release (it re-runs the gate cleanly if it ever returns).
      for (const id of [...released]) {
        if (!present.has(id))
          released.delete(id)
      }

      const held = new Set<string>()
      // Walk in document order. A still-loading row -- and a ready row we are
      // holding behind one -- blocks every LATER row from revealing ahead of it.
      // Already-released rows never block and are never re-held.
      let blocked = false
      for (const id of ids) {
        if (released.has(id) && !isLoading(id))
          continue
        if (isLoading(id)) {
          // Genuinely still loading: hidden by its own skeleton, and it gates the
          // rest of the cohort. Forget any stale release (a height-key churn
          // pushed it back into loading).
          released.delete(id)
          blocked = true
          continue
        }
        // Loaded but not yet released.
        if (blocked)
          held.add(id)
        else
          released.add(id)
      }
      return held
    },
    new Set<string>(),
    { equals: shallowEqualSets },
  )
}
