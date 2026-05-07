import type { TilePopAction } from './TileActionsMenu'
import type { SplitOrientation, TileCloseMode } from '~/stores/layout.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GridPopoverHostProvider } from './GridPopoverHost'
import { Tile } from './Tile'

interface RenderOpts {
  closeMode?: TileCloseMode
  canSplit?: boolean
  canMakeGrid?: boolean
  isFocused?: boolean
  onSplit?: (direction: SplitOrientation) => void
  onMakeGrid?: (rows: number, cols: number) => void
  onClose?: () => void
  onFocus?: () => void
  pop?: TilePopAction
}

function renderTile(opts: RenderOpts = {}) {
  return render(() => (
    <GridPopoverHostProvider>
      <Tile
        tileId="tile-1"
        isFocused={opts.isFocused ?? false}
        actions={{
          closeMode: opts.closeMode ?? { kind: 'tile' },
          canSplit: opts.canSplit ?? true,
          canMakeGrid: opts.canMakeGrid ?? false,
          onSplit: opts.onSplit ?? (() => {}),
          onMakeGrid: opts.onMakeGrid ?? (() => {}),
          onClose: opts.onClose ?? (() => {}),
        }}
        tabBar={<div data-testid="tab-bar">tabs</div>}
        onFocus={opts.onFocus ?? (() => {})}
        pop={opts.pop}
      >
        <div data-testid="tile-content">content</div>
      </Tile>
    </GridPopoverHostProvider>
  ))
}

describe('tile', () => {
  it('renders the tab bar and children', () => {
    renderTile()
    expect(screen.getByTestId('tab-bar')).toBeInTheDocument()
    expect(screen.getByTestId('tile-content')).toBeInTheDocument()
  })

  it('hides the close button when closeMode is "none"', () => {
    renderTile({ closeMode: { kind: 'none' } })
    expect(screen.queryByTestId('close-tile')).toBeNull()
    expect(screen.queryByTestId('close-grid')).toBeNull()
  })

  it('shows close-tile when closeMode is "tile"', () => {
    renderTile({ closeMode: { kind: 'tile' } })
    expect(screen.getByTestId('close-tile')).toBeInTheDocument()
    expect(screen.queryByTestId('close-grid')).toBeNull()
  })

  it('shows close-grid (with "Close grid" tooltip) when closeMode is "grid"', () => {
    renderTile({ closeMode: { kind: 'grid', gridId: 'g1' } })
    const btn = screen.getByTestId('close-grid')
    expect(btn).toBeInTheDocument()
    expect(btn.getAttribute('title') ?? btn.getAttribute('aria-label')).toBe('Close grid')
  })

  it('shows split-horizontal and split-vertical buttons when canSplit is true', () => {
    renderTile({ canSplit: true })
    expect(screen.getByTestId('split-horizontal')).toBeInTheDocument()
    expect(screen.getByTestId('split-vertical')).toBeInTheDocument()
  })

  it('hides split buttons when canSplit is false (max-depth tile)', () => {
    renderTile({ canSplit: false })
    expect(screen.queryByTestId('split-horizontal')).toBeNull()
    expect(screen.queryByTestId('split-vertical')).toBeNull()
  })

  it('shows the make-grid button when canMakeGrid is true', () => {
    renderTile({ canMakeGrid: true })
    expect(screen.getByTestId('make-grid')).toBeInTheDocument()
  })

  it('hides the make-grid button when canMakeGrid is false', () => {
    renderTile({ canMakeGrid: false })
    expect(screen.queryByTestId('make-grid')).toBeNull()
  })

  it('invokes onMakeGrid with the chosen size when a popover cell is clicked', () => {
    const onMakeGrid = vi.fn()
    renderTile({ canMakeGrid: true, onMakeGrid })
    fireEvent.click(screen.getByTestId('make-grid'))
    // Click cell (1,2) → 2x3 grid.
    fireEvent.click(screen.getByTestId('grid-size-cell-1-2'))
    expect(onMakeGrid).toHaveBeenCalledWith(2, 3)
  })

  it('invokes onSplit("horizontal") when the split-horizontal button is clicked', () => {
    const onSplit = vi.fn()
    renderTile({ canSplit: true, onSplit })
    fireEvent.click(screen.getByTestId('split-horizontal'))
    expect(onSplit).toHaveBeenCalledTimes(1)
    expect(onSplit).toHaveBeenCalledWith('horizontal')
  })

  it('invokes onSplit("vertical") when the split-vertical button is clicked', () => {
    const onSplit = vi.fn()
    renderTile({ canSplit: true, onSplit })
    fireEvent.click(screen.getByTestId('split-vertical'))
    expect(onSplit).toHaveBeenCalledTimes(1)
    expect(onSplit).toHaveBeenCalledWith('vertical')
  })

  it('invokes onClose when the close button is clicked (tile mode)', () => {
    const onClose = vi.fn()
    renderTile({ closeMode: { kind: 'tile' }, onClose })
    fireEvent.click(screen.getByTestId('close-tile'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('invokes onClose when the close button is clicked (grid mode)', () => {
    const onClose = vi.fn()
    renderTile({ closeMode: { kind: 'grid', gridId: 'g1' }, onClose })
    fireEvent.click(screen.getByTestId('close-grid'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('clicking a button does not bubble to onFocus', () => {
    // Tile's onClick (focus) is on the wrapper. Action buttons stopPropagation
    // so that splitting/closing a tile does not also trigger focus.
    const onFocus = vi.fn()
    const onSplit = vi.fn()
    renderTile({ canSplit: true, onFocus, onSplit })
    fireEvent.click(screen.getByTestId('split-horizontal'))
    expect(onSplit).toHaveBeenCalledTimes(1)
    expect(onFocus).not.toHaveBeenCalled()
  })

  it('clicking the tile body invokes onFocus', () => {
    const onFocus = vi.fn()
    renderTile({ onFocus })
    fireEvent.click(screen.getByTestId('tile-content'))
    expect(onFocus).toHaveBeenCalled()
  })

  it('renders pop-out button when the action carries the pop-out testId', () => {
    renderTile({ pop: { label: 'Pop out to floating window', testId: 'pop-out-button', onClick: () => {} } })
    expect(screen.getByTestId('pop-out-button')).toBeInTheDocument()
    expect(screen.queryByTestId('pop-in-button')).toBeNull()
  })

  it('renders pop-in button when the action carries the pop-in testId', () => {
    renderTile({ pop: { label: 'Pop in to main window', testId: 'pop-in-button', onClick: () => {} } })
    expect(screen.getByTestId('pop-in-button')).toBeInTheDocument()
    expect(screen.queryByTestId('pop-out-button')).toBeNull()
  })

  it('renders neither pop-out nor pop-in when pop is absent', () => {
    renderTile({})
    expect(screen.queryByTestId('pop-out-button')).toBeNull()
    expect(screen.queryByTestId('pop-in-button')).toBeNull()
  })

  it('does not render the bordered split-actions strip when no buttons would show', () => {
    // canSplit=false + canMakeGrid=false + closeMode='none' + no
    // pop-out/pop-in handlers means the strip would otherwise be empty
    // but still draw its left border + padding next to the tab bar.
    const { container } = renderTile({
      closeMode: { kind: 'none' },
      canSplit: false,
      canMakeGrid: false,
    })
    // No action testids present.
    expect(screen.queryByTestId('split-horizontal')).toBeNull()
    expect(screen.queryByTestId('make-grid')).toBeNull()
    expect(screen.queryByTestId('close-tile')).toBeNull()
    expect(screen.queryByTestId('pop-out-button')).toBeNull()
    // The tab-bar row should contain only the filler (no second flex
    // child for the actions strip).
    const tabBarRow = container.querySelector('[data-testid="tab-bar"]')!.parentElement!.parentElement!
    expect(tabBarRow.children.length).toBe(1)
  })

  it('throws a clear error when make-grid is clicked without a GridPopoverHostProvider', () => {
    // The shell mounts `GridPopoverHostProvider` once; if a Tile ever renders
    // outside that provider (e.g. a future test added without the wrapper)
    // we want the failure to be loud, not a silent noop. This pins the
    // contract introduced when the local-fallback popover was removed from
    // Tile.tsx.
    //
    // jsdom routes uncaught errors from event handlers through `window.error`
    // rather than re-throwing from `dispatchEvent`, so we listen there
    // instead of relying on `toThrowError`.
    const captured: ErrorEvent[] = []
    const onError = (e: ErrorEvent) => {
      captured.push(e)
      e.preventDefault()
    }
    window.addEventListener('error', onError)
    try {
      const onMakeGrid = vi.fn()
      render(() => (
        <Tile
          tileId="tile-1"
          isFocused={false}
          actions={{
            closeMode: { kind: 'tile' },
            canSplit: false,
            canMakeGrid: true,
            onSplit: () => {},
            onMakeGrid,
            onClose: () => {},
          }}
          tabBar={<div data-testid="tab-bar">tabs</div>}
          onFocus={() => {}}
        >
          <div data-testid="tile-content">content</div>
        </Tile>
      ))
      // The button mounts fine — it's only the request to open the popover
      // that demands a host. Click surfaces the throw via window.error.
      fireEvent.click(screen.getByTestId('make-grid'))
      expect(captured).toHaveLength(1)
      expect(captured[0]?.error?.message).toMatch(/GridPopoverHostProvider/)
      expect(onMakeGrid).not.toHaveBeenCalled()
    }
    finally {
      window.removeEventListener('error', onError)
    }
  })
})
