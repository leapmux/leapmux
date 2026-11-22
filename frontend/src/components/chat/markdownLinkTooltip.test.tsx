import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { dismissActiveTooltip, HIDE_DELAY_MS, SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { createMarkdownLinkTooltip } from './markdownLinkTooltip'

describe('createMarkdownLinkTooltip', () => {
  afterEach(() => {
    dismissActiveTooltip()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('mounts lazily on first hover or focus, ignores touch, and tracks hover-over-focus precedence', () => {
    vi.useFakeTimers()
    const [enabled, setEnabled] = createSignal(true)
    const [localActive, setLocalActive] = createSignal(true)
    const [html, setHtml] = createSignal('<p><a href="https://example.com">Web</a> <span data-local-link="./a.md">Local</span></p>')
    const [bodyEl, setBodyEl] = createSignal<HTMLDivElement | undefined>(undefined)

    const resolveTarget = (node: EventTarget | null, root: HTMLElement): HTMLElement | undefined => {
      if (!(node instanceof Node))
        return undefined
      const el = node instanceof Element
        ? node
        : node.parentElement
      if (!el || !root.contains(el))
        return undefined
      const remote = el.closest<HTMLAnchorElement>('a[href]')
      if (remote && root.contains(remote))
        return remote
      if (!localActive())
        return undefined
      const local = el.closest<HTMLElement>('[data-local-link]')
      return local && root.contains(local)
        ? local
        : undefined
    }

    let controller!: ReturnType<typeof createMarkdownLinkTooltip>
    const { container } = render(() => {
      controller = createMarkdownLinkTooltip({
        bodyEl,
        html,
        enabled,
        hasTargets: () => Boolean(html()),
        localActive,
        resolveTarget,
        isLocalTarget: target => !(target instanceof HTMLAnchorElement),
        getTooltipText: target => (target instanceof HTMLAnchorElement
          ? target.href
          : `/repo/${target.getAttribute('data-local-link')}`),
      })
      return (
        <>
          <div
            ref={(el) => {
              el.innerHTML = html()
              setBodyEl(el)
            }}
            onPointerOver={controller.onPointerOver}
            onPointerOut={controller.onPointerOut}
            onFocusIn={controller.onFocusIn}
            onFocusOut={controller.onFocusOut}
          />
          <controller.Popover />
        </>
      )
    })

    const body = container.firstElementChild as HTMLDivElement
    const web = body.querySelector('a') as HTMLAnchorElement
    const local = body.querySelector('[data-local-link]') as HTMLElement

    // Touch does not mount or open a tooltip.
    web.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'touch' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS + 100)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()

    // Focus opens tooltip on the remote link; hovering the local link takes precedence.
    fireEvent.focusIn(web)
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('https://example.com/')

    local.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/./a.md')

    // Deactivating local links dismisses the local tooltip synchronously and hands over to focused remote link.
    setLocalActive(false)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('https://example.com/')

    // Disabling suppresses and unmounts the active tooltip immediately.
    setEnabled(false)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    setEnabled(true)

    // Re-hovering after external dismissal reopens cleanly, and HTML replacement dismisses immediately.
    web.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('https://example.com/')

    dismissActiveTooltip()
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    web.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('https://example.com/')

    setHtml('<p><a href="https://two.example">Two</a></p>')
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()

    // Direct dismiss resets state cleanly during HIDE_DELAY_MS.
    body.innerHTML = html()
    const two = body.querySelector('a') as HTMLAnchorElement
    two.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('https://two.example/')
    two.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(HIDE_DELAY_MS / 2)
    controller.dismiss()
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })
})
