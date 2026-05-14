import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import { createEffect, on } from 'solid-js'
import { containsTileId, firstLeafId } from '~/stores/layout.store'

/**
 * Wire the focus invariant for `layoutStore.focusedTileId`.
 *
 * `layoutStore` only sees the main tree; tiles owned by a floating
 * window aren't in its projected root. Enforcing the invariant inside
 * `layoutStore` therefore mis-classified every floating-window tile as
 * "gone" and snapped focus back to the main tree's first leaf the
 * moment a user clicked a tab or tile inside a floating window.
 *
 * This hook lives a layer up where both stores are visible. The
 * focused tile is considered valid if it appears in the main tree OR
 * in any live floating window; only if it's missing from both do we
 * fall back to the main tree's first leaf (matching the previous
 * "tile closed by a peer" recovery behaviour).
 *
 * Reactivity scope: explicit `on([focusedTileId, root])` so the effect
 * only re-runs when either of those two signals changes — not on every
 * unrelated layoutStore mutation (ratio updates during a drag, focus
 * cycles within the same root, etc.). Without this narrowing, every
 * tile-resize tick triggered a tree walk that almost always exited at
 * the `containsTileId` check.
 */
export interface UseFocusInvariantArgs {
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
}

export function useFocusInvariant(args: UseFocusInvariantArgs): void {
  const { layoutStore, floatingWindowStore } = args
  createEffect(
    on(
      [() => layoutStore.state.focusedTileId, () => layoutStore.state.root],
      ([f, root]) => {
        if (!f)
          return
        if (containsTileId(root, f))
          return
        if (floatingWindowStore.getWindowForTile(f) !== null)
          return
        layoutStore.setFocusedTile(firstLeafId(root) ?? null)
      },
    ),
  )
}
