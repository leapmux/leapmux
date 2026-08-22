import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { flush } from '~/test-support/async'
import { pointerEvent } from '~/test-support/pointer'
import { SelectionQuotePopover } from './SelectionQuotePopover'

// `~/lib/clipboard` announces a failed write itself. Mocked here so the copy
// tests can assert that the user was told, and so the real helper does not
// reach for a toast host that jsdom has not installed.
const showWarnToastWithLoggedCause = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToastWithLoggedCause }))

const originalGetSelection = window.getSelection

/**
 * Stand in for the platform's Selection.
 *
 * A default range comes with it, carrying the same text. `selectionInside` (see
 * ~/lib/textSelection.ts) reads the RANGE's text rather than the selection's,
 * because only the range's is independent of `user-select` -- so a mock that
 * answers `toString` alone reads as live to the component and as empty to the
 * predicate. A case that needs geometry overrides `getRangeAt` itself.
 */
function mockSelection(selection: Partial<Selection> | null): void {
  const text = selection ? String(selection.toString?.() ?? '') : ''
  const withRange = selection && {
    rangeCount: 1,
    getRangeAt: () => ({ toString: () => text }) as unknown as Range,
    ...selection,
  }
  vi.spyOn(window, 'getSelection').mockReturnValue(withRange as Selection | null)
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

  // The wrapper is where the multi-tap gesture lives, because it IS the prose:
  // the chat transcript and both file views mount it around the text a reader is
  // allowed to select. See ~/lib/tapSelect.ts.
  describe('the finger gesture it hosts', () => {
    const TEXT = 'the quick brown fox'

    /** jsdom has no layout, so the two things layout would answer are supplied. */
    function withLayout(node: Text, rect = { left: 10, right: 90, top: 40, bottom: 60 }) {
      document.caretPositionFromPoint = ((x: number) => ({
        offsetNode: node,
        offset: Math.max(0, Math.min(Math.round(x), node.data.length)),
        getClientRect: () => null,
      })) as unknown as typeof document.caretPositionFromPoint
      Object.defineProperty(Range.prototype, 'getClientRects', {
        configurable: true,
        value: () => [{ ...rect, width: rect.right - rect.left, height: rect.bottom - rect.top }],
      })
      vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
        cb(0)
        return 1
      })
      vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    }

    function doubleTap(el: Element, x: number) {
      for (let tap = 0; tap < 2; tap++) {
        el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch', x, y: 50 }))
        el.dispatchEvent(pointerEvent('pointerup', { pointerType: 'touch', x, y: 50 }))
      }
    }

    afterEach(() => {
      Reflect.deleteProperty(document, 'caretPositionFromPoint')
      Reflect.deleteProperty(Range.prototype, 'getClientRects')
      window.getSelection()?.removeAllRanges()
    })

    it('selects a word and offers to copy or quote it', () => {
      render(() => (
        <SelectionQuotePopover onQuote={vi.fn()}>
          <p data-testid="selectable">{TEXT}</p>
        </SelectionQuotePopover>
      ))
      const paragraph = screen.getByTestId('selectable')
      withLayout(paragraph.firstChild as Text)

      doubleTap(paragraph, TEXT.indexOf('brown') + 2)

      expect(window.getSelection()?.toString()).toBe('brown')
      expect(screen.getByTestId('quote-selection-popover')).toBeInTheDocument()
    })

    it('quotes what the gesture selected', () => {
      const onQuote = vi.fn()
      render(() => (
        <SelectionQuotePopover onQuote={onQuote}>
          <p data-testid="selectable">{TEXT}</p>
        </SelectionQuotePopover>
      ))
      const paragraph = screen.getByTestId('selectable')
      withLayout(paragraph.firstChild as Text)

      doubleTap(paragraph, TEXT.indexOf('brown') + 2)
      fireEvent.click(screen.getByTestId('quote-selection-button'))

      // No line range: a chat quote carries markdown, and only the file views
      // report the `startLine`/`endLine` the other two arguments hold.
      expect(onQuote).toHaveBeenCalledWith('brown', undefined, undefined)
    })

    // The popover is a CHILD of the element the gesture attaches to, so a third
    // tap that lands on it would otherwise select its own button label.
    it('keeps the gesture off its own buttons', () => {
      render(() => (
        <SelectionQuotePopover onQuote={vi.fn()}>
          <p data-testid="selectable">{TEXT}</p>
        </SelectionQuotePopover>
      ))
      const paragraph = screen.getByTestId('selectable')
      withLayout(paragraph.firstChild as Text)
      doubleTap(paragraph, TEXT.indexOf('brown') + 2)

      expect(screen.getByTestId('quote-selection-popover')).toHaveAttribute('data-no-tap-select')
    })

    it('drops the gesture when it unmounts', () => {
      const rendered = render(() => (
        <SelectionQuotePopover onQuote={vi.fn()}>
          <p data-testid="selectable">{TEXT}</p>
        </SelectionQuotePopover>
      ))
      const paragraph = screen.getByTestId('selectable')
      withLayout(paragraph.firstChild as Text)
      rendered.unmount()

      doubleTap(paragraph, TEXT.indexOf('brown') + 2)

      expect(window.getSelection()?.toString()).toBe('')
    })
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
        toString: () => 'selected text',
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
