import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { GridSizePopover } from '~/components/shell/GridSizePopover'

function setup(overrides?: { onSelect?: (rows: number, cols: number) => void, onClose?: () => void }) {
  const [open, setOpen] = createSignal(true)
  const onSelect = overrides?.onSelect ?? vi.fn()
  const onClose = overrides?.onClose ?? vi.fn()
  let anchorEl: HTMLButtonElement | null = null
  render(() => (
    <>
      <button
        ref={(el) => { anchorEl = el }}
        data-testid="anchor"
      >
        anchor
      </button>
      <GridSizePopover
        open={open}
        anchor={() => anchorEl}
        onSelect={onSelect}
        onClose={onClose}
      />
    </>
  ))
  return { onSelect, onClose, setOpen }
}

describe('gridSizePopover hover-grid', () => {
  it('renders 54 cells (6×9)', () => {
    setup()
    expect(screen.getByTestId('grid-size-popover')).toBeInTheDocument()
    expect(screen.getByTestId('grid-size-cell-0-0')).toBeInTheDocument()
    expect(screen.getByTestId('grid-size-cell-5-8')).toBeInTheDocument()
  })

  it('clicking cell (r, c) calls onSelect(r+1, c+1)', () => {
    const { onSelect } = setup()
    fireEvent.click(screen.getByTestId('grid-size-cell-2-3'))
    expect(onSelect).toHaveBeenCalledWith(3, 4)
  })

  it('hovering a cell highlights the upper-left rectangle and updates the label', () => {
    setup()
    fireEvent.pointerEnter(screen.getByTestId('grid-size-cell-1-2'))
    expect(screen.getByTestId('grid-size-label')).toHaveTextContent('2 × 3')
    expect(screen.getByTestId('grid-size-cell-0-0').getAttribute('data-highlighted')).toBe('true')
    expect(screen.getByTestId('grid-size-cell-1-2').getAttribute('data-highlighted')).toBe('true')
    // Cells outside the (1,2) rectangle are not highlighted.
    expect(screen.getByTestId('grid-size-cell-2-2').getAttribute('data-highlighted')).toBe('false')
    expect(screen.getByTestId('grid-size-cell-1-3').getAttribute('data-highlighted')).toBe('false')
  })
})

describe('gridSizePopover manual entry', () => {
  it('typing 8 × 5 and clicking Create calls onSelect(8, 5)', () => {
    const { onSelect } = setup()
    const rowsInput = screen.getByTestId('grid-size-rows-input') as HTMLInputElement
    const colsInput = screen.getByTestId('grid-size-cols-input') as HTMLInputElement
    fireEvent.input(rowsInput, { target: { value: '8' } })
    fireEvent.input(colsInput, { target: { value: '5' } })
    fireEvent.click(screen.getByTestId('grid-size-create-button'))
    expect(onSelect).toHaveBeenCalledWith(8, 5)
  })

  it('create button is disabled for out-of-range values', () => {
    setup()
    const rowsInput = screen.getByTestId('grid-size-rows-input') as HTMLInputElement
    const colsInput = screen.getByTestId('grid-size-cols-input') as HTMLInputElement
    const create = screen.getByTestId('grid-size-create-button') as HTMLButtonElement
    expect(create.disabled).toBe(true) // empty
    fireEvent.input(rowsInput, { target: { value: '0' } })
    fireEvent.input(colsInput, { target: { value: '5' } })
    expect(create.disabled).toBe(true)
    fireEvent.input(rowsInput, { target: { value: '21' } })
    expect(create.disabled).toBe(true)
    fireEvent.input(rowsInput, { target: { value: '8' } })
    expect(create.disabled).toBe(false)
  })
})

describe('gridSizePopover keyboard', () => {
  it('escape calls onClose', () => {
    const { onClose } = setup()
    fireEvent.keyDown(screen.getByTestId('grid-size-popover'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
