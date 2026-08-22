import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { flush } from '~/test-support/async'
import { SelectionQuotePopover } from './SelectionQuotePopover'

// `~/lib/clipboard` announces a failed write itself. Mocked here so the copy
// tests can assert that the user was told, and so the real helper does not
// reach for a toast host that jsdom has not installed.
const showWarnToastWithLoggedCause = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToastWithLoggedCause }))

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

  it('clears active selection state when copying without waiting for selectionchange', async () => {
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
      // The handler awaits the write, so its three closing statements land a
      // microtask after the click rather than inside it.
      await flush()

      expect(writeText).toHaveBeenCalledWith('selected text')
      expect(removeAllRanges).toHaveBeenCalled()
      expect(onSelectionActiveChange).toHaveBeenLastCalledWith(false)
      expect(screen.queryByTestId('quote-selection-popover')).not.toBeInTheDocument()
      expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
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

  // A non-secure origin (plain http:// on a LAN, which is how the app is read on
  // a phone) exposes no `navigator.clipboard` at all, and jsdom implements no
  // `execCommand` for the fallback to reach either -- so nothing is copied.
  //
  // Clearing the highlight and closing the popover is the app SAYING "copied".
  // Doing it here reported a copy that never happened, which is the whole reason
  // an unreachable clipboard read as a dead button. The selection stays up, and
  // the Copy button with it, so the user can try again once told why.
  it('keeps the selection and the popover when the write does not land', async () => {
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
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
      await flush()

      expect(removeAllRanges).not.toHaveBeenCalled()
      expect(onSelectionActiveChange).toHaveBeenLastCalledWith(true)
      expect(screen.getByTestId('quote-selection-popover')).toBeInTheDocument()
      // ...and the user is told why, rather than left to guess at a button that
      // appears to do nothing.
      expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
      expect(showWarnToastWithLoggedCause.mock.calls[0]![0]).toContain('Could not copy')
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
