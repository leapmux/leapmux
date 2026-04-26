import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { Tile } from './Tile'

interface RenderOpts {
  canClose?: boolean
  canSplit?: boolean
  isFocused?: boolean
  onSplitHorizontal?: () => void
  onSplitVertical?: () => void
  onClose?: () => void
  onFocus?: () => void
  onPopOut?: () => void
  onPopIn?: () => void
}

function renderTile(opts: RenderOpts = {}) {
  return render(() => (
    <Tile
      tileId="tile-1"
      isFocused={opts.isFocused ?? false}
      canClose={opts.canClose ?? false}
      canSplit={opts.canSplit ?? true}
      tabBar={<div data-testid="tab-bar">tabs</div>}
      onFocus={opts.onFocus ?? (() => {})}
      onSplitHorizontal={opts.onSplitHorizontal ?? (() => {})}
      onSplitVertical={opts.onSplitVertical ?? (() => {})}
      onClose={opts.onClose ?? (() => {})}
      onPopOut={opts.onPopOut}
      onPopIn={opts.onPopIn}
    >
      <div data-testid="tile-content">content</div>
    </Tile>
  ))
}

describe('tile', () => {
  it('renders the tab bar and children', () => {
    renderTile()
    expect(screen.getByTestId('tab-bar')).toBeInTheDocument()
    expect(screen.getByTestId('tile-content')).toBeInTheDocument()
  })

  it('hides the close-tile button when canClose is false (single tile case)', () => {
    renderTile({ canClose: false })
    expect(screen.queryByTestId('close-tile')).toBeNull()
  })

  it('shows the close-tile button when canClose is true (multi-tile case)', () => {
    renderTile({ canClose: true })
    expect(screen.getByTestId('close-tile')).toBeInTheDocument()
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

  it('invokes onSplitHorizontal when the split-horizontal button is clicked', () => {
    const onSplitHorizontal = vi.fn()
    renderTile({ canSplit: true, onSplitHorizontal })
    fireEvent.click(screen.getByTestId('split-horizontal'))
    expect(onSplitHorizontal).toHaveBeenCalledTimes(1)
  })

  it('invokes onSplitVertical when the split-vertical button is clicked', () => {
    const onSplitVertical = vi.fn()
    renderTile({ canSplit: true, onSplitVertical })
    fireEvent.click(screen.getByTestId('split-vertical'))
    expect(onSplitVertical).toHaveBeenCalledTimes(1)
  })

  it('invokes onClose when the close-tile button is clicked', () => {
    const onClose = vi.fn()
    renderTile({ canClose: true, onClose })
    fireEvent.click(screen.getByTestId('close-tile'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('clicking a button does not bubble to onFocus', () => {
    // Tile's onClick (focus) is on the wrapper. Action buttons stopPropagation
    // so that splitting/closing a tile does not also trigger focus.
    const onFocus = vi.fn()
    const onSplitHorizontal = vi.fn()
    renderTile({ canSplit: true, onFocus, onSplitHorizontal })
    fireEvent.click(screen.getByTestId('split-horizontal'))
    expect(onSplitHorizontal).toHaveBeenCalledTimes(1)
    expect(onFocus).not.toHaveBeenCalled()
  })

  it('clicking the tile body invokes onFocus', () => {
    const onFocus = vi.fn()
    renderTile({ onFocus })
    fireEvent.click(screen.getByTestId('tile-content'))
    expect(onFocus).toHaveBeenCalled()
  })

  it('renders pop-out button when onPopOut is provided', () => {
    renderTile({ onPopOut: () => {} })
    expect(screen.getByTestId('pop-out-button')).toBeInTheDocument()
    expect(screen.queryByTestId('pop-in-button')).toBeNull()
  })

  it('renders pop-in button when onPopIn is provided', () => {
    renderTile({ onPopIn: () => {} })
    expect(screen.getByTestId('pop-in-button')).toBeInTheDocument()
    expect(screen.queryByTestId('pop-out-button')).toBeNull()
  })

  it('renders neither pop-out nor pop-in when neither callback is provided', () => {
    renderTile({})
    expect(screen.queryByTestId('pop-out-button')).toBeNull()
    expect(screen.queryByTestId('pop-in-button')).toBeNull()
  })
})
