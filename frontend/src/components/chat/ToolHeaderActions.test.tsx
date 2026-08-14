import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { ToolHeaderActions } from './ToolHeaderActions'

describe('toolHeaderActions', () => {
  afterEach(() => cleanup())

  /**
   * `buildMessageActions` returns fresh objects whenever any input changes, and a
   * copy flipping to "Copied" is such a change. Rebuilding the elements from that
   * list would replace the button the user just clicked, taking its focus with it.
   */
  it('keeps each button element across a copied-state flip', () => {
    const [copied, setCopied] = createSignal(false)
    render(() => (
      <ToolHeaderActions
        caller={{ onReply: () => {} }}
        layout={{ onCopyJson: () => {}, get jsonCopied() { return copied() } }}
      />
    ))

    const copyBefore = screen.getByTestId('message-copy-json')
    const quoteBefore = screen.getByTestId('message-quote')
    // `IconButton` routes `title` through `<Tooltip ariaLabel>`, so the label
    // lands on `aria-label` rather than the OS `title` attribute.
    expect(copyBefore).toHaveAttribute('aria-label', 'Copy Raw JSON')

    setCopied(true)

    // Same nodes, updated in place.
    expect(screen.getByTestId('message-copy-json')).toBe(copyBefore)
    expect(screen.getByTestId('message-quote')).toBe(quoteBefore)
    expect(copyBefore).toHaveAttribute('aria-label', 'Copied')
  })

  it('adds and removes a button when the row gains or loses that action', () => {
    const [quotable, setQuotable] = createSignal(false)
    render(() => (
      <ToolHeaderActions
        caller={{ get onReply() { return quotable() ? () => {} : undefined } }}
        layout={{ onCopyJson: () => {} }}
      />
    ))

    expect(screen.queryByTestId('message-quote')).not.toBeInTheDocument()

    setQuotable(true)
    expect(screen.getByTestId('message-quote')).toBeInTheDocument()

    setQuotable(false)
    expect(screen.queryByTestId('message-quote')).not.toBeInTheDocument()
  })
})
