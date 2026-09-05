import type { MessageAction } from './messageActions'
import type { MessageContextMenuHost } from './MessageContextMenuHost'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MessageContextMenuHostProvider, useMessageContextMenu } from './MessageContextMenuHost'

// The jsdom popover stubs (showPopover/hidePopover/togglePopover plus the
// `:popover-open` matches interceptor) come from vitest.setup.ts, which runs
// before every test file.

afterEach(() => cleanup())

function action(id: MessageAction['id'], run = () => {}): MessageAction {
  return { id, label: id, icon: () => null, run }
}

function dangerAction(id: MessageAction['id'], run = () => {}): MessageAction {
  return { ...action(id, run), danger: true }
}

/** Render the provider and hand back the host a descendant row would get. */
function renderHost() {
  const captured: { host?: MessageContextMenuHost } = {}

  function Row() {
    captured.host = useMessageContextMenu()
    return <div data-testid="row" />
  }

  render(() => (
    <MessageContextMenuHostProvider>
      <Row />
    </MessageContextMenuHostProvider>
  ))

  return captured
}

describe('messageContextMenuHost', () => {
  it('provides a host to its descendants', () => {
    const captured = renderHost()
    expect(captured.host).toBeDefined()
  })

  it('returns undefined with no provider, so a bare MessageBubble still renders', () => {
    const captured: { host?: MessageContextMenuHost } = {}

    function Row() {
      captured.host = useMessageContextMenu()
      return <div />
    }

    render(() => <Row />)
    expect(captured.host).toBeUndefined()
  })

  it('mounts no items until a row asks it to open', () => {
    renderHost()
    expect(screen.queryByRole('menuitem')).not.toBeInTheDocument()
  })

  it('renders the actions the requesting row supplied', () => {
    const captured = renderHost()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      actions: [action('copy-json'), action('quote')],
    })

    expect(screen.getByTestId('message-menu-copy-json')).toBeInTheDocument()
    expect(screen.getByTestId('message-menu-quote')).toBeInTheDocument()
    expect(screen.queryByTestId('message-menu-expand')).not.toBeInTheDocument()
  })

  it('lists the actions in the reverse of the toolbar order', () => {
    const captured = renderHost()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      // The toolbar's own order: broadest copy first, view toggles last.
      actions: [action('copy-json'), action('copy-markdown'), action('quote'), action('expand')],
    })

    const ids = [...screen.getByTestId('message-context-menu').querySelectorAll('[data-testid^="message-menu-"]')]
      .map(el => el.getAttribute('data-testid')!.replace('message-menu-', ''))

    // Read top-down from the cursor, the narrowest and most-used actions come first.
    expect(ids).toEqual(['expand', 'quote', 'copy-markdown', 'copy-json'])
  })

  /**
   * Reversal puts the most-used action directly under the cursor. A destructive
   * one must never land there.
   */
  it('pins a destructive action to the foot, out of the reversal', () => {
    const captured = renderHost()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      actions: [action('copy-json'), action('quote'), action('diff-view'), dangerAction('expand')],
    })

    const ids = [...screen.getByTestId('message-context-menu').querySelectorAll('[data-testid^="message-menu-"]')]
      .map(el => el.getAttribute('data-testid')!.replace('message-menu-', ''))

    expect(ids).toEqual(['diff-view', 'quote', 'copy-json', 'expand'])
    // And behind a rule, like every other danger item in the app.
    expect(screen.getByTestId('message-context-menu').querySelector('hr')).toBeInTheDocument()
  })

  it('runs a destructive action once when activated', () => {
    const captured = renderHost()
    const run = vi.fn()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      actions: [action('quote'), dangerAction('expand', run)],
    })
    fireEvent.click(screen.getByTestId('message-menu-expand'))

    expect(run).toHaveBeenCalledTimes(1)
  })

  it('leads with the send time when the message has one', () => {
    const captured = renderHost()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      actions: [action('quote')],
      createdAt: '2025-01-15T10:00:00.000Z',
    })

    const info = screen.getByTestId('message-menu-info')
    expect(info).toHaveTextContent('Sent:')
    // Ahead of every action, like the worker and file menus' info blocks.
    expect(info.compareDocumentPosition(screen.getByTestId('message-menu-quote')))
      .toBe(Node.DOCUMENT_POSITION_FOLLOWING)
  })

  it('omits the info block when the message carries no timestamp', () => {
    const captured = renderHost()

    captured.host!.open({ press: { clientX: 150, clientY: 60 }, actions: [action('quote')] })

    expect(screen.queryByTestId('message-menu-info')).not.toBeInTheDocument()
    // And no orphan separator above the first action.
    expect(screen.getByTestId('message-context-menu').querySelector('hr')).not.toBeInTheDocument()
  })

  it('runs an action when its item is activated', () => {
    const captured = renderHost()
    const run = vi.fn()

    captured.host!.open({ press: { clientX: 150, clientY: 60 }, actions: [action('quote', run)] })
    fireEvent.click(screen.getByTestId('message-menu-quote'))

    expect(run).toHaveBeenCalledTimes(1)
  })

  it('shows the popover at the press point, not at the row it came from', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)
    try {
      const captured = renderHost()
      const popover = screen.getByTestId('message-context-menu')
      popover.getBoundingClientRect = () => ({
        top: 0,
        bottom: 150,
        left: 0,
        right: 200,
        width: 200,
        height: 150,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })
      const show = vi.spyOn(popover, 'showPopover')

      captured.host!.open({ press: { clientX: 150, clientY: 60 }, actions: [action('quote')] })

      expect(show).toHaveBeenCalled()
      // A chat row can be hundreds of pixels tall, so the menu must land on the
      // cursor and not at the row's bottom edge.
      expect(popover.style.left).toBe('150px')
      expect(popover.style.top).toBe('60px')
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('re-anchors in place when a second row opens while the menu is up', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)
    try {
      const captured = renderHost()
      const popover = screen.getByTestId('message-context-menu')
      popover.getBoundingClientRect = () => ({
        top: 0,
        bottom: 150,
        left: 0,
        right: 200,
        width: 200,
        height: 150,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })
      const hide = vi.spyOn(popover, 'hidePopover')

      captured.host!.open({ press: { clientX: 150, clientY: 60 }, actions: [action('quote')] })
      expect(popover.style.left).toBe('150px')

      captured.host!.open({ press: { clientX: 300, clientY: 90 }, actions: [action('copy-json')] })

      // No close/reopen pass: the menu stays up, re-anchors to the new press,
      // and swaps content synchronously. DropdownMenu's open effect tracks the
      // anchor, so a still-true `open` with a new anchor repositions in place.
      expect(hide).not.toHaveBeenCalled()
      expect(popover.style.left).toBe('300px')
      expect(popover.style.top).toBe('90px')
      expect(screen.getByTestId('message-menu-copy-json')).toBeInTheDocument()
      expect(screen.queryByTestId('message-menu-quote')).not.toBeInTheDocument()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })

  it('unmounts the items when the host closes', () => {
    const captured = renderHost()

    captured.host!.open({
      press: { clientX: 150, clientY: 60 },
      actions: [action('quote')],
    })
    expect(screen.getByTestId('message-menu-quote')).toBeInTheDocument()

    captured.host!.close()
    expect(screen.queryByTestId('message-menu-quote')).not.toBeInTheDocument()
  })
})
