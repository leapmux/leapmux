import type { JSX } from 'solid-js'
import Copy from 'lucide-solid/icons/copy'
import Quote from 'lucide-solid/icons/quote'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
import { extractLineRange, extractSelectionMarkdown } from '~/lib/quoteUtils'
import * as styles from './SelectionQuotePopover.css'

interface SelectionQuotePopoverProps {
  containerRef: HTMLElement | undefined
  onQuote: (selectedText: string, startLine?: number, endLine?: number) => void
  children: JSX.Element
}

export function SelectionQuotePopover(props: SelectionQuotePopoverProps): JSX.Element {
  let wrapperRef!: HTMLDivElement
  let popoverRef: HTMLDivElement | undefined
  const [visible, setVisible] = createSignal(false)
  const [position, setPosition] = createSignal({ top: 0, left: 0 })

  const hidePopover = () => setVisible(false)

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
    requestAnimationFrame(() => {
      const selection = window.getSelection()
      if (!selection || selection.isCollapsed || !selection.toString().trim())
        return

      const container = props.containerRef ?? wrapperRef
      if (!container)
        return

      // Check that the selection is within our container
      if (!container.contains(selection.anchorNode) || !container.contains(selection.focusNode))
        return

      // Position the popover above the end of the selection
      const range = selection.getRangeAt(0)
      const rects = range.getClientRects()
      if (rects.length === 0)
        return

      const lastRect = rects[rects.length - 1]
      const containerRect = wrapperRef.getBoundingClientRect()

      setPosition({
        top: lastRect.top - containerRect.top - 34,
        left: lastRect.right - containerRect.left,
      })
      setVisible(true)
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
    hidePopover()
  }

  onMount(() => {
    const el = wrapperRef
    el.addEventListener('mousedown', handleMouseDown)
    el.addEventListener('mouseup', handleMouseUp)
    onCleanup(() => {
      el.removeEventListener('mousedown', handleMouseDown)
      el.removeEventListener('mouseup', handleMouseUp)
    })
  })

  return (
    <div ref={wrapperRef} style={{ position: 'relative' }}>
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
            <Quote size={14} />
            Quote
          </button>
          <button
            class={styles.quoteButton}
            onClick={handleCopyClick}
            data-testid="copy-selection-button"
          >
            <Copy size={14} />
            Copy
          </button>
        </div>
      </Show>
    </div>
  )
}
