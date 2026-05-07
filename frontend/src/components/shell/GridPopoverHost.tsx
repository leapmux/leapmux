import type { ParentComponent } from 'solid-js'
import { createContext, createSignal, useContext } from 'solid-js'
import { GridSizePopover } from './GridSizePopover'

/**
 * One-shot request from a tile asking the singleton popover to open with a
 * specific anchor. The popover invokes `onSelect` once when the user picks a
 * size and then closes itself; light-dismiss / Escape close it without
 * invoking `onSelect`.
 */
export interface GridPopoverRequest {
  anchor: HTMLElement
  onSelect: (rows: number, cols: number) => void
}

export interface GridPopoverHost {
  open: (request: GridPopoverRequest) => void
}

const GridPopoverContext = createContext<GridPopoverHost | undefined>(undefined)

/**
 * Read the host from context. Returns `undefined` when no
 * `GridPopoverHostProvider` ancestor exists; callers should fall back to a
 * locally-mounted popover (e.g. unit-test render trees that don't wrap in
 * the shell).
 */
export function useGridPopover(): GridPopoverHost | undefined {
  return useContext(GridPopoverContext)
}

/**
 * Provide a single shared `<GridSizePopover>` for every descendant tile.
 *
 * Rationale: each tile that has `canMakeGrid=true` would otherwise mount
 * its own popover under `<Show>`, and `DropdownMenu` always renders
 * children into the `popover="auto"` element — the contents (54 hover-grid
 * cells, two manual-entry inputs, the Create button) are hidden, not
 * unmounted. A 3×3 main layout meant ~486 always-mounted cells per
 * workspace. Routing every tile through this singleton means the contents
 * exist exactly once.
 *
 * The provider tracks one `GridPopoverRequest` at a time. Tiles call
 * `host.open({ anchor, onSelect })`; the popover positions against the
 * given anchor and calls back when the user picks a size. A second
 * `host.open(...)` call (e.g. user clicks a different tile's button while
 * the popover is open) replaces the request — the popover re-anchors.
 */
export const GridPopoverHostProvider: ParentComponent = (props) => {
  const [request, setRequest] = createSignal<GridPopoverRequest | null>(null)

  const host: GridPopoverHost = {
    open: req => setRequest(req),
  }

  return (
    <GridPopoverContext.Provider value={host}>
      {props.children}
      <GridSizePopover
        open={() => request() !== null}
        anchor={() => request()?.anchor ?? null}
        onSelect={(rows, cols) => {
          // Capture the current request before clearing so a stale callback
          // doesn't fire if the user opens a different tile's popover
          // mid-select. The popover hides itself via popoverEl.hidePopover();
          // its toggle event then calls onClose → setRequest(null), so this
          // pre-clear is just defensive.
          const current = request()
          setRequest(null)
          current?.onSelect(rows, cols)
        }}
        onClose={() => setRequest(null)}
      />
    </GridPopoverContext.Provider>
  )
}
