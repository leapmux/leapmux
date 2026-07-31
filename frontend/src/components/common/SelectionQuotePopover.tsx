import type { JSX } from 'solid-js'
import Copy from 'lucide-solid/icons/copy'
import Quote from 'lucide-solid/icons/quote'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { extractLineRange, extractSelectionMarkdown } from '~/lib/quoteUtils'
import * as styles from './SelectionQuotePopover.css'

interface SelectionQuotePopoverProps {
  class?: string
  onQuote: (selectedText: string, startLine?: number, endLine?: number) => void
  /**
   * Reports whether the document selection is currently inside this wrapper.
   * Chat uses this to freeze syntax-highlight DOM swaps while the browser owns
   * a live text selection; replacing selected text nodes clears the selection.
   */
  onSelectionActiveChange?: (active: boolean) => void
  children: JSX.Element
}

export function SelectionQuotePopover(props: SelectionQuotePopoverProps): JSX.Element {
  let wrapperRef!: HTMLDivElement
  let popoverRef: HTMLDivElement | undefined
  const [visible, setVisible] = createSignal(false)
  const [position, setPosition] = createSignal({ top: 0, left: 0 })
  let selectionActive = false
  let selectionFrame: number | undefined
  let clampFrame: number | undefined

  const hidePopover = () => setVisible(false)

  const cancelScheduledFrames = () => {
    if (selectionFrame !== undefined) {
      cancelAnimationFrame(selectionFrame)
      selectionFrame = undefined
    }
    if (clampFrame !== undefined) {
      cancelAnimationFrame(clampFrame)
      clampFrame = undefined
    }
  }

  const setSelectionActive = (active: boolean) => {
    if (selectionActive === active)
      return
    selectionActive = active
    props.onSelectionActiveChange?.(active)
  }

  const selectionInsideContainer = (selection: Selection, container: HTMLElement): boolean => {
    const anchorNode = selection.anchorNode
    const focusNode = selection.focusNode
    return !!anchorNode && !!focusNode && container.contains(anchorNode) && container.contains(focusNode)
  }

  /**
   * `wrapperRef` -- the element this component itself renders -- IS the container.
   *
   * There used to be a `containerRef` prop meant to narrow this to the caller's own
   * content element, with `wrapperRef` as a fallback. It never once took effect: all
   * three call sites passed a bare `let` binding, which Solid's JSX transform treats
   * as a STATIC prop and captures at creation time -- before the `ref` callback that
   * assigns it has run. So the prop was permanently `undefined` and the fallback was
   * the only path. Naming `wrapperRef` directly is what the code has always done, and
   * it is also the honest answer: it wraps `props.children` and it is what the
   * mousedown/mouseup listeners below are attached to, so "the selection is inside
   * this popover's subtree" and "the selection is inside the container" are the same
   * question. Its only extra content is the popover's own two buttons, which hold no
   * selectable prose.
   */
  const updateSelectionActive = (): Selection | null => {
    const selection = window.getSelection()
    if (!selection || selection.isCollapsed || !selection.toString().trim() || !wrapperRef || !selectionInsideContainer(selection, wrapperRef)) {
      setSelectionActive(false)
      return null
    }
    setSelectionActive(true)
    return selection
  }

  const handleMouseDown = (e: MouseEvent) => {
    // Don't hide the popover when clicking the popover itself (Quote button),
    // and prevent the browser from clearing the text selection before click fires.
    if (popoverRef?.contains(e.target as Node)) {
      e.preventDefault()
      return
    }
    hidePopover()
  }

  const handleMouseUp = () => {
    // Small delay to let the browser finalize the selection
    cancelScheduledFrames()
    selectionFrame = requestAnimationFrame(() => {
      selectionFrame = undefined
      const selection = updateSelectionActive()
      if (!selection)
        return

      // Position the popover above the end of the selection
      const range = selection.getRangeAt(0)
      const rects = [...range.getClientRects()]
      if (rects.length === 0) {
        setSelectionActive(false)
        return
      }

      const lastRect = rects.at(-1)!

      // Place at the end of the selection, then clamp so it stays on-screen.
      let left = lastRect.right
      let top = lastRect.top - 34
      setPosition({ top, left })
      setVisible(true)

      // After the popover renders, clamp to viewport bounds.
      clampFrame = requestAnimationFrame(() => {
        clampFrame = undefined
        if (!popoverRef)
          return
        const rect = popoverRef.getBoundingClientRect()
        const vw = window.innerWidth
        const vh = window.innerHeight
        if (rect.right > vw)
          left = Math.max(0, vw - rect.width)
        if (top < 0)
          top = lastRect.bottom + 4
        if (top + rect.height > vh)
          top = Math.max(0, vh - rect.height)
        setPosition({ top, left })
      })
    })
  }

  const handleCopyClick = (e: MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    const selection = window.getSelection()
    if (!selection || selection.isCollapsed)
      return

    const lineRange = extractLineRange(selection)
    const text = lineRange ? selection.toString() : extractSelectionMarkdown(selection)
    void navigator.clipboard.writeText(text)
    selection.removeAllRanges()
    setSelectionActive(false)
    hidePopover()
  }

  const handleQuoteClick = (e: MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    const selection = window.getSelection()
    if (!selection || selection.isCollapsed)
      return

    const lineRange = extractLineRange(selection)
    // Use plain text for file view quotes (with line ranges), markdown for chat quotes
    const text = lineRange ? selection.toString() : extractSelectionMarkdown(selection)
    props.onQuote(text, lineRange?.startLine, lineRange?.endLine)
    selection.removeAllRanges()
    setSelectionActive(false)
    hidePopover()
  }

  // Hide the popover when the selection is cleared (e.g. by focusing another input).
  const handleSelectionChange = () => {
    const selection = updateSelectionActive()
    if (!visible())
      return
    if (!selection)
      hidePopover()
  }

  onMount(() => {
    const el = wrapperRef
    el.addEventListener('mousedown', handleMouseDown)
    el.addEventListener('mouseup', handleMouseUp)
    document.addEventListener('selectionchange', handleSelectionChange)
    onCleanup(() => {
      el.removeEventListener('mousedown', handleMouseDown)
      el.removeEventListener('mouseup', handleMouseUp)
      document.removeEventListener('selectionchange', handleSelectionChange)
      cancelScheduledFrames()
      setSelectionActive(false)
    })
  })

  return (
    <div ref={wrapperRef} class={props.class}>
      {props.children}
      <Show when={visible()}>
        <div
          ref={popoverRef}
          class={styles.popover}
          style={{ top: `${position().top}px`, left: `${position().left}px` }}
          data-testid="quote-selection-popover"
        >
          <button
            class={styles.quoteButton}
            onClick={handleQuoteClick}
            data-testid="quote-selection-button"
          >
            <Icon icon={Quote} size="sm" />
            Quote
          </button>
          <button
            class={styles.quoteButton}
            onClick={handleCopyClick}
            data-testid="copy-selection-button"
          >
            <Icon icon={Copy} size="sm" />
            Copy
          </button>
        </div>
      </Show>
    </div>
  )
}
