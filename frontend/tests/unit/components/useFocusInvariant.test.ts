import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { useFocusInvariant } from '~/components/shell/useFocusInvariant'
import { setCRDTBridge } from '~/lib/crdt'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { installTestBridge } from '../helpers/crdtBridge'

afterEach(() => setCRDTBridge(null))

/**
 * The invariant lives where both stores are visible. Floating-window
 * tiles must NOT be treated as "gone" — the original layout-store
 * effect did that and snapped focus back to the main tree on every
 * click inside a floating window.
 */
describe('useFocusInvariant', () => {
  it('preserves focus on a tile that lives inside a floating window', async () => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          installTestBridge({ rootTileId: 'main' })
          const layoutStore = createLayoutStore()
          const floatingWindowStore = createFloatingWindowStore()
          useFocusInvariant({ layoutStore, floatingWindowStore })

          const created = floatingWindowStore.addWindow()
          expect(created).not.toBeNull()
          const { tileId: floatingTileId } = created!

          layoutStore.setFocusedTile(floatingTileId)

          queueMicrotask(() => {
            try {
              expect(layoutStore.focusedTileId()).toBe(floatingTileId)
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  it('falls back to the main first leaf when the focused tile is gone from both stores', async () => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          installTestBridge({ rootTileId: 'main' })
          const layoutStore = createLayoutStore()
          const floatingWindowStore = createFloatingWindowStore()
          useFocusInvariant({ layoutStore, floatingWindowStore })

          layoutStore.setFocusedTile('ghost-tile')

          queueMicrotask(() => {
            try {
              expect(layoutStore.focusedTileId()).toBe('main')
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  it('leaves focus alone when the tile is already in the main tree', async () => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          installTestBridge({ rootTileId: 'main' })
          const layoutStore = createLayoutStore()
          const floatingWindowStore = createFloatingWindowStore()
          useFocusInvariant({ layoutStore, floatingWindowStore })

          layoutStore.setFocusedTile('main')

          queueMicrotask(() => {
            try {
              expect(layoutStore.focusedTileId()).toBe('main')
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  /**
   * Pins the reactivity-narrowing: the effect must wake up only when
   * focusedTileId or root changes — NOT on every layout mutation. We
   * verify by counting `getWindowForTile` calls (the effect's
   * floatingWindowStore probe). With explicit `on([focusedTileId, root])`
   * an unrelated focus toggle that ends with the same focused tile
   * triggers exactly one extra effect run, not the firehose of "every
   * layout mutation" the old `createEffect(() => ...)` form produced.
   */
  it('only re-runs on focusedTileId or root changes', async () => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          installTestBridge({ rootTileId: 'main' })
          const layoutStore = createLayoutStore()
          const floatingWindowStore = createFloatingWindowStore()
          const realGetWindowForTile = floatingWindowStore.getWindowForTile
          let calls = 0
          floatingWindowStore.getWindowForTile = (tileId: string) => {
            calls++
            return realGetWindowForTile(tileId)
          }
          useFocusInvariant({ layoutStore, floatingWindowStore })

          // Set focus to main once — fires the effect, but main is in
          // the tree so getWindowForTile is never called (containsTileId
          // returns true and the function early-exits).
          layoutStore.setFocusedTile('main')

          queueMicrotask(() => {
            try {
              const before = calls

              // Re-set focus to the same value — no signal change, no
              // re-run.
              layoutStore.setFocusedTile('main')

              queueMicrotask(() => {
                try {
                  expect(calls).toBe(before)

                  // Now set focus to a tile NOT in the main tree — the
                  // effect must wake up (one getWindowForTile call).
                  layoutStore.setFocusedTile('ghost-tile')

                  queueMicrotask(() => {
                    try {
                      expect(calls).toBe(before + 1)
                      dispose()
                      resolve()
                    }
                    catch (err) {
                      dispose()
                      reject(err)
                    }
                  })
                }
                catch (err) {
                  dispose()
                  reject(err)
                }
              })
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })
})
