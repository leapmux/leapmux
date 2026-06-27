import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SelectionQuotePopover } from './SelectionQuotePopover'

const originalGetSelection = window.getSelection

function mockSelection(selection: Partial<Selection> | null): void {
  vi.spyOn(window, 'getSelection').mockReturnValue(selection as Selection | null)
}

describe('selection quote popover', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    Object.defineProperty(window, 'getSelection', {
      configurable: true,
      value: originalGetSelection,
    })
  })

  it('reports active and cleared selections inside its content', () => {
    const onSelectionActiveChange = vi.fn()
    render(() => (
      <SelectionQuotePopover
        containerRef={undefined}
        onQuote={vi.fn()}
        onSelectionActiveChange={onSelectionActiveChange}
      >
        <p data-testid="selectable">selected text</p>
      </SelectionQuotePopover>
    ))
    const textNode = screen.getByTestId('selectable').firstChild!

    mockSelection({
      isCollapsed: false,
      anchorNode: textNode,
      focusNode: textNode,
      toString: () => 'selected text',
    })
    fireEvent(document, new Event('selectionchange'))

    mockSelection({
      isCollapsed: true,
      anchorNode: textNode,
      focusNode: textNode,
      toString: () => '',
    })
    fireEvent(document, new Event('selectionchange'))

    expect(onSelectionActiveChange).toHaveBeenNthCalledWith(1, true)
    expect(onSelectionActiveChange).toHaveBeenNthCalledWith(2, false)
  })

  it('forwards a class to the selection wrapper', () => {
    const { container } = render(() => (
      <SelectionQuotePopover
        class="non-shrinking-scroll-root"
        containerRef={undefined}
        onQuote={vi.fn()}
      >
        <p>content</p>
      </SelectionQuotePopover>
    ))

    expect(container.firstElementChild).toHaveClass('non-shrinking-scroll-root')
  })

  it('cancels pending selection frames on cleanup', () => {
    const callbacks = new Map<number, FrameRequestCallback>()
    let nextFrame = 1
    const requestFrame = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      const id = nextFrame++
      callbacks.set(id, cb)
      return id
    })
    const cancelFrame = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation((id) => {
      callbacks.delete(id)
    })
    const onSelectionActiveChange = vi.fn()
    const rendered = render(() => (
      <SelectionQuotePopover
        containerRef={undefined}
        onQuote={vi.fn()}
        onSelectionActiveChange={onSelectionActiveChange}
      >
        <p data-testid="selectable">selected text</p>
      </SelectionQuotePopover>
    ))
    const textNode = screen.getByTestId('selectable').firstChild!

    mockSelection({
      isCollapsed: false,
      anchorNode: textNode,
      focusNode: textNode,
      toString: () => 'selected text',
      getRangeAt: () => ({
        getClientRects: () => [{ left: 0, right: 10, top: 10, bottom: 20 }],
      }) as unknown as Range,
    })

    fireEvent.mouseUp(screen.getByTestId('selectable'))
    rendered.unmount()

    for (const cb of callbacks.values())
      cb(0)

    expect(cancelFrame).toHaveBeenCalledWith(1)
    expect(onSelectionActiveChange).not.toHaveBeenCalledWith(true)

    requestFrame.mockRestore()
    cancelFrame.mockRestore()
  })

  it('clears active selection state when copying without waiting for selectionchange', () => {
    const writeText = vi.fn()
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const requestFrame = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      cb(0)
      return 1
    })
    const cancelFrame = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    const removeAllRanges = vi.fn()
    const onSelectionActiveChange = vi.fn()
    try {
      render(() => (
        <SelectionQuotePopover
          containerRef={undefined}
          onQuote={vi.fn()}
          onSelectionActiveChange={onSelectionActiveChange}
        >
          <p data-testid="selectable">selected text</p>
        </SelectionQuotePopover>
      ))
      const textNode = screen.getByTestId('selectable').firstChild!
      const selectedFragment = document.createDocumentFragment()
      selectedFragment.append(document.createTextNode('selected text'))

      mockSelection({
        isCollapsed: false,
        rangeCount: 1,
        anchorNode: textNode,
        focusNode: textNode,
        toString: () => 'selected text',
        removeAllRanges,
        getRangeAt: () => ({
          getClientRects: () => [{ left: 0, right: 10, top: 10, bottom: 20 }],
          cloneContents: () => selectedFragment.cloneNode(true),
        }) as unknown as Range,
      })

      fireEvent.mouseUp(screen.getByTestId('selectable'))
      fireEvent.click(screen.getByTestId('copy-selection-button'))

      expect(writeText).toHaveBeenCalledWith('selected text')
      expect(removeAllRanges).toHaveBeenCalled()
      expect(onSelectionActiveChange).toHaveBeenLastCalledWith(false)
    }
    finally {
      if (clipboardDescriptor)
        Object.defineProperty(navigator, 'clipboard', clipboardDescriptor)
      else
        Reflect.deleteProperty(navigator, 'clipboard')
      requestFrame.mockRestore()
      cancelFrame.mockRestore()
    }
  })
})
