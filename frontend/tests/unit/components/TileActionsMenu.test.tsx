import type { TileActions } from '~/components/shell/TileActionsMenu'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TileActionsMenu } from '~/components/shell/TileActionsMenu'

function makeActions(overrides: Partial<TileActions> = {}): TileActions {
  return {
    canSplit: false,
    canMakeGrid: false,
    closeMode: { kind: 'none' },
    onSplit: () => {},
    onMakeGrid: () => {},
    onClose: () => {},
    ...overrides,
  }
}

describe('tileActionsMenu', () => {
  it('hides split items when canSplit is false', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canSplit: false })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    expect(screen.queryByText('Split vertically')).toBeNull()
    expect(screen.queryByText('Split horizontally')).toBeNull()
  })

  it('shows both split items when canSplit is true', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canSplit: true })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    expect(screen.getByText('Split vertically')).toBeDefined()
    expect(screen.getByText('Split horizontally')).toBeDefined()
  })

  it('hides make-grid when canMakeGrid is false', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canMakeGrid: false })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    expect(screen.queryByTestId('make-grid-menu-item')).toBeNull()
  })

  it('shows make-grid with the make-grid-menu-item testid when canMakeGrid is true', () => {
    const onMakeGridClick = vi.fn()
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canMakeGrid: true })}
        onMakeGridClick={onMakeGridClick}
        makeGridLabel="Make a 2×2 grid"
      />
    ))
    const item = screen.getByTestId('make-grid-menu-item')
    expect(item).toBeDefined()
    expect(item.textContent).toContain('Make a 2×2 grid')
    fireEvent.click(item)
    expect(onMakeGridClick).toHaveBeenCalledOnce()
  })

  it('hides close item when closeMode is none', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ closeMode: { kind: 'none' } })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    expect(screen.queryByTestId('close-grid-menu-item')).toBeNull()
    expect(screen.queryByTestId('close-tile-menu-item')).toBeNull()
  })

  it('emits close-tile-menu-item when closeMode is tile', () => {
    const onClose = vi.fn()
    render(() => (
      <TileActionsMenu
        actions={makeActions({ closeMode: { kind: 'tile' }, onClose })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    const item = screen.getByTestId('close-tile-menu-item')
    expect(item.textContent).toContain('Close tile')
    fireEvent.click(item)
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('emits close-grid-menu-item when closeMode is grid', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ closeMode: { kind: 'grid', gridId: 'g1' } })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    const item = screen.getByTestId('close-grid-menu-item')
    expect(item.textContent).toContain('Close grid')
  })

  it('renders the pop-out menu item from the action label', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions()}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
        pop={{ label: 'Pop out to floating window', testId: 'pop-out-button', onClick: () => {} }}
      />
    ))
    expect(screen.getByText('Pop out to floating window')).toBeDefined()
    expect(screen.queryByText('Pop in to main window')).toBeNull()
  })

  it('renders the pop-in menu item from the action label', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions()}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
        pop={{ label: 'Pop in to main window', testId: 'pop-in-button', onClick: () => {} }}
      />
    ))
    expect(screen.getByText('Pop in to main window')).toBeDefined()
    expect(screen.queryByText('Pop out to floating window')).toBeNull()
  })

  it('omits pop-out / pop-in when pop is absent', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions()}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    expect(screen.queryByText('Pop out to floating window')).toBeNull()
    expect(screen.queryByText('Pop in to main window')).toBeNull()
  })

  it('without withIcons, split / make-grid / close items have no leading <svg> icon', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canSplit: true, canMakeGrid: true, closeMode: { kind: 'tile' } })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
      />
    ))
    const splitH = screen.getByText('Split vertically').closest('button')!
    const splitV = screen.getByText('Split horizontally').closest('button')!
    const grid = screen.getByTestId('make-grid-menu-item')
    const close = screen.getByTestId('close-tile-menu-item')
    for (const btn of [splitH, splitV, grid, close])
      expect(btn.querySelector('svg')).toBeNull()
  })

  it('with withIcons, split / make-grid / close items have exactly one leading <svg> icon', () => {
    render(() => (
      <TileActionsMenu
        actions={makeActions({ canSplit: true, canMakeGrid: true, closeMode: { kind: 'tile' } })}
        onMakeGridClick={() => {}}
        makeGridLabel="Make grid"
        withIcons
      />
    ))
    const splitH = screen.getByText('Split vertically').closest('button')!
    const splitV = screen.getByText('Split horizontally').closest('button')!
    const grid = screen.getByTestId('make-grid-menu-item')
    const close = screen.getByTestId('close-tile-menu-item')
    for (const btn of [splitH, splitV, grid, close])
      expect(btn.querySelectorAll('svg')).toHaveLength(1)
  })
})
