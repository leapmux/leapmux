import type { Accessor, Component } from 'solid-js'
import { batch, createSignal, Index } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { MAX_GRID_DIMENSION } from '~/stores/layout.store'
import * as styles from './GridSizePopover.css'

const HOVER_GRID_ROWS = 6
const HOVER_GRID_COLS = 9
// Stable empty array indexed for the cell `<Index>`. Hoisted so each
// reactive re-render reuses the same reference instead of allocating
// HOVER_GRID_ROWS * HOVER_GRID_COLS slots per evaluation.
const HOVER_GRID_CELLS = Array.from({ length: HOVER_GRID_ROWS * HOVER_GRID_COLS })

const ARROW_DELTAS: Record<string, [number, number]> = {
  ArrowRight: [0, 1],
  ArrowLeft: [0, -1],
  ArrowDown: [1, 0],
  ArrowUp: [-1, 0],
}

interface GridSizePopoverProps {
  /** Reactive open/close state. */
  open: Accessor<boolean>
  /** Anchor element accessor. */
  anchor: Accessor<HTMLElement | null | undefined>
  /** Called with the chosen dimensions; the popover does not close itself. */
  onSelect: (rows: number, cols: number) => void
  /** Called when the user dismisses the popover (Escape, outside click). */
  onClose: () => void
}

export const GridSizePopover: Component<GridSizePopoverProps> = (props) => {
  let popoverEl: HTMLElement | undefined
  let gridEl: HTMLDivElement | undefined

  const [hoverRow, setHoverRow] = createSignal(-1)
  const [hoverCol, setHoverCol] = createSignal(-1)
  const [manualRows, setManualRows] = createSignal('')
  const [manualCols, setManualCols] = createSignal('')

  const parsedManualRows = () => Number(manualRows())
  const parsedManualCols = () => Number(manualCols())

  const isManualValid = () => {
    const r = parsedManualRows()
    const c = parsedManualCols()
    return Number.isInteger(r) && Number.isInteger(c)
      && r >= 1 && c >= 1 && r <= MAX_GRID_DIMENSION && c <= MAX_GRID_DIMENSION
  }

  const handleHoverGridKey = (e: KeyboardEvent) => {
    // Escape is handled by DropdownMenu's wrapper; we only own arrow / Enter.
    const row = hoverRow()
    const col = hoverCol()
    const delta = ARROW_DELTAS[e.key]
    if (delta) {
      e.preventDefault()
      // From an unset selection (-1, -1) the first arrow key snaps to (0, 0);
      // subsequent keys move one cell along the axis, clamped to the grid.
      const [dr, dc] = delta
      const newRow = row < 0 ? 0 : Math.max(0, Math.min(row + dr, HOVER_GRID_ROWS - 1))
      const newCol = col < 0 ? 0 : Math.max(0, Math.min(col + dc, HOVER_GRID_COLS - 1))
      batch(() => {
        setHoverRow(newRow)
        setHoverCol(newCol)
      })
      return
    }
    if (e.key === 'Enter' && row >= 0 && col >= 0) {
      e.preventDefault()
      props.onSelect(row + 1, col + 1)
    }
  }

  const commitManual = () => {
    if (!isManualValid())
      return
    props.onSelect(parsedManualRows(), parsedManualCols())
  }

  /**
   * Submit the manual-entry pair on Enter; ignore other keys so the native
   * number-input keystrokes (digits, arrows, backspace) still work.
   */
  const onManualEnter = (e: KeyboardEvent) => {
    if (e.key !== 'Enter')
      return
    e.preventDefault()
    commitManual()
  }

  return (
    <DropdownMenu
      as="div"
      anchorRef={() => props.anchor() ?? undefined}
      open={props.open}
      popoverRef={(el) => { popoverEl = el }}
      class={styles.popover}
      data-testid="grid-size-popover"
      onToggle={(opening) => {
        if (opening) {
          // Reset state and focus the inner grid so arrow keys work
          // immediately after open.
          batch(() => {
            setHoverRow(-1)
            setHoverCol(-1)
            setManualRows('')
            setManualCols('')
          })
          queueMicrotask(() => gridEl?.focus())
        }
        else {
          props.onClose()
        }
      }}
    >
      <div class={styles.sizeLabel} data-testid="grid-size-label">
        {hoverRow() >= 0 && hoverCol() >= 0
          ? `${hoverRow() + 1} × ${hoverCol() + 1}`
          : 'Pick a size'}
      </div>
      <div
        ref={gridEl}
        class={styles.grid}
        tabIndex={0}
        role="grid"
        aria-label="Choose grid size"
        style={{ 'grid-template-columns': `repeat(${HOVER_GRID_COLS}, 20px)` }}
        onKeyDown={handleHoverGridKey}
        onPointerLeave={() => {
          batch(() => {
            setHoverRow(-1)
            setHoverCol(-1)
          })
        }}
      >
        <Index each={HOVER_GRID_CELLS}>
          {(_, i) => {
            const r = Math.floor(i / HOVER_GRID_COLS)
            const c = i % HOVER_GRID_COLS
            return (
              <div
                role="gridcell"
                class={styles.cell}
                data-testid={`grid-size-cell-${r}-${c}`}
                data-highlighted={r <= hoverRow() && c <= hoverCol() ? 'true' : 'false'}
                onPointerEnter={() => {
                  batch(() => {
                    setHoverRow(r)
                    setHoverCol(c)
                  })
                }}
                onClick={() => {
                  props.onSelect(r + 1, c + 1)
                  popoverEl?.hidePopover()
                }}
              />
            )
          }}
        </Index>
      </div>
      {/* Stop click propagation so the wrapper's auto-dismiss-on-click
          doesn't fire while the user interacts with the manual-entry
          inputs. The Create button still bubbles its click so clicking
          Create selects + closes in one step. */}
      <div class={styles.manualEntry} onClick={e => e.stopPropagation()}>
        <input
          class={styles.manualInput}
          type="number"
          min={1}
          max={MAX_GRID_DIMENSION}
          value={manualRows()}
          data-testid="grid-size-rows-input"
          aria-label="Rows"
          onInput={(e) => { setManualRows(e.currentTarget.value) }}
          onKeyDown={onManualEnter}
        />
        <span class={styles.manualSeparator}>×</span>
        <input
          class={styles.manualInput}
          type="number"
          min={1}
          max={MAX_GRID_DIMENSION}
          value={manualCols()}
          data-testid="grid-size-cols-input"
          aria-label="Columns"
          onInput={(e) => { setManualCols(e.currentTarget.value) }}
          onKeyDown={onManualEnter}
        />
        <button
          type="button"
          class="small"
          data-testid="grid-size-create-button"
          disabled={!isManualValid()}
          onClick={(e) => {
            // Don't let the wrapper's auto-dismiss intercept; we close
            // explicitly after onSelect runs so the parent sees a valid
            // post-select state on the close-toggle.
            e.stopPropagation()
            commitManual()
            popoverEl?.hidePopover()
          }}
        >
          Create
        </button>
      </div>
    </DropdownMenu>
  )
}
