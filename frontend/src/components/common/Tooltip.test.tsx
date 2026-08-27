import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as gesture from './contextMenuGesture'
import { dismissActiveTooltip, Tooltip } from './Tooltip'

describe('tooltip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('applies aria-describedby while visible', () => {
    render(() => (
      <Tooltip text="Tooltip text">
        <button type="button">Trigger</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Trigger' })
    expect(button).not.toHaveAttribute('aria-describedby')

    fireEvent.mouseEnter(button)
    vi.advanceTimersByTime(700)

    const tooltip = screen.getByRole('tooltip', { hidden: true })
    expect(button).toHaveAttribute('aria-describedby', tooltip.id)

    fireEvent.mouseLeave(button)
    vi.advanceTimersByTime(100)
    expect(button).not.toHaveAttribute('aria-describedby')
  })

  it('dismisses immediately on click so it does not linger over a triggered menu', () => {
    render(() => (
      <Tooltip text="Tooltip text">
        <button type="button">Trigger</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Trigger' })

    fireEvent.mouseEnter(button)
    vi.advanceTimersByTime(700)
    expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()

    fireEvent.click(button)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    expect(button).not.toHaveAttribute('aria-describedby')
  })

  it('uses tooltip text as aria-label when ariaLabel is true', () => {
    render(() => (
      <Tooltip text="Zoom in" ariaLabel>
        <button type="button">
          +
        </button>
      </Tooltip>
    ))

    expect(screen.getByRole('button')).toHaveAttribute('aria-label', 'Zoom in')
  })

  it('uses the explicit ariaLabel when provided', () => {
    render(() => (
      <Tooltip text="Tooltip text" ariaLabel="Explicit label">
        <button type="button">
          +
        </button>
      </Tooltip>
    ))

    expect(screen.getByRole('button')).toHaveAttribute('aria-label', 'Explicit label')
  })

  it('leaves visible text targets without an aria-label by default', () => {
    render(() => (
      <Tooltip text="Helpful details">
        <button type="button">Visible label</button>
      </Tooltip>
    ))

    expect(screen.getByRole('button', { name: 'Visible label' })).not.toHaveAttribute('aria-label')
  })

  describe('showWhen=clipped', () => {
    const stubRect = (el: Element, rect: Partial<DOMRect>) => {
      const full: DOMRect = {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        width: 0,
        height: 0,
        toJSON: () => '',
        ...rect,
      } as DOMRect
      Object.defineProperty(el, 'getBoundingClientRect', {
        value: () => full,
        configurable: true,
      })
    }

    it('suppresses the tooltip when the target fits without clipping', () => {
      render(() => (
        <Tooltip text="Tooltip text" showWhen="clipped">
          <button type="button">Trigger</button>
        </Tooltip>
      ))

      const button = screen.getByRole('button', { name: 'Trigger' })
      // Inside viewport, no overflow ancestors, scrollWidth==clientWidth==0.
      stubRect(button, { left: 10, top: 10, right: 60, bottom: 30, width: 50, height: 20 })

      fireEvent.mouseEnter(button)
      vi.advanceTimersByTime(700)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
      expect(button).not.toHaveAttribute('aria-describedby')
    })

    it('shows the tooltip when the target truncates its own text', () => {
      render(() => (
        <Tooltip text="Tooltip text" showWhen="clipped">
          {/* jsdom doesn't expand the `overflow` shorthand, so set the longhand. */}
          <button type="button" style={{ 'overflow-x': 'hidden', 'overflow-y': 'hidden' }}>
            A very long label that gets cut off
          </button>
        </Tooltip>
      ))

      const button = screen.getByRole('button')
      stubRect(button, { left: 10, top: 10, right: 60, bottom: 30, width: 50, height: 20 })
      Object.defineProperty(button, 'scrollWidth', { value: 200, configurable: true })
      Object.defineProperty(button, 'clientWidth', { value: 50, configurable: true })

      fireEvent.mouseEnter(button)
      vi.advanceTimersByTime(700)

      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
    })

    it('shows the tooltip when an <input> value overflows its width', () => {
      // <input> always clips its value internally, even though browsers
      // typically report overflow as `visible`. The clip-detector should
      // still catch this case.
      render(() => (
        <Tooltip text="long path" showWhen="clipped">
          <input type="text" value="long path" />
        </Tooltip>
      ))

      const input = screen.getByRole('textbox')
      stubRect(input, { left: 0, top: 0, right: 50, bottom: 20, width: 50, height: 20 })
      Object.defineProperty(input, 'scrollWidth', { value: 200, configurable: true })
      Object.defineProperty(input, 'clientWidth', { value: 50, configurable: true })

      fireEvent.mouseEnter(input)
      vi.advanceTimersByTime(700)

      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
    })

    it('shows the tooltip when an overflow ancestor clips the target', () => {
      render(() => (
        <div style={{ 'overflow-x': 'hidden', 'overflow-y': 'hidden', 'width': '100px' }}>
          <Tooltip text="Tooltip text" showWhen="clipped">
            <button type="button">Trigger</button>
          </Tooltip>
        </div>
      ))

      const button = screen.getByRole('button')
      const container = button.closest('div')!
      // Button rect extends past the container's right edge.
      stubRect(button, { left: 0, top: 0, right: 200, bottom: 30, width: 200, height: 30 })
      stubRect(container, { left: 0, top: 0, right: 100, bottom: 30, width: 100, height: 30 })

      fireEvent.mouseEnter(button)
      vi.advanceTimersByTime(700)

      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
    })

    it('shows the tooltip when the target is hidden behind a scrollbar', () => {
      // Container's bounding rect right edge is 100, but its client area
      // (excluding the 15px scrollbar) ends at 85. Target's right edge
      // sits at 95 — visible inside the bounding rect, but covered by
      // the scrollbar. Should still report as clipped.
      render(() => (
        <div style={{ 'overflow-x': 'auto', 'overflow-y': 'auto', 'width': '100px' }}>
          <Tooltip text="Tooltip text" showWhen="clipped">
            <button type="button">Trigger</button>
          </Tooltip>
        </div>
      ))

      const button = screen.getByRole('button')
      const container = button.closest('div')!
      stubRect(button, { left: 0, top: 0, right: 95, bottom: 30, width: 95, height: 30 })
      stubRect(container, { left: 0, top: 0, right: 100, bottom: 30, width: 100, height: 30 })
      Object.defineProperty(container, 'clientLeft', { value: 0, configurable: true })
      Object.defineProperty(container, 'clientTop', { value: 0, configurable: true })
      Object.defineProperty(container, 'clientWidth', { value: 85, configurable: true })
      Object.defineProperty(container, 'clientHeight', { value: 30, configurable: true })

      fireEvent.mouseEnter(button)
      vi.advanceTimersByTime(700)

      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
    })

    // A row scrolled part-way out of a vertical list FITS its own box. Treating
    // that as clipped would show a tooltip that repeats the visible label, on
    // every sidebar row that happens to sit at the edge of its scroller.
    it('ignores an ancestor that cuts the target vertically only', () => {
      render(() => (
        <div style={{ 'overflow-x': 'hidden', 'overflow-y': 'auto', 'height': '30px' }}>
          <Tooltip text="Tooltip text" showWhen="clipped">
            <button type="button">Row label</button>
          </Tooltip>
        </div>
      ))

      const button = screen.getByRole('button')
      const container = button.closest('div')!
      // The target sits ABOVE the scroller's client box, and fits it sideways.
      stubRect(button, { left: 10, top: 10, right: 60, bottom: 30, width: 50, height: 20 })
      stubRect(container, { left: 0, top: 20, right: 100, bottom: 50, width: 100, height: 30 })
      Object.defineProperty(container, 'clientLeft', { value: 0, configurable: true })
      Object.defineProperty(container, 'clientTop', { value: 0, configurable: true })
      Object.defineProperty(container, 'clientWidth', { value: 100, configurable: true })
      Object.defineProperty(container, 'clientHeight', { value: 30, configurable: true })

      fireEvent.mouseEnter(button)
      vi.advanceTimersByTime(700)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })
  })

  /**
   * Touch pointers get no tooltip. A tap once opened it — with no hover
   * delay — but the tooltip covered the very element that the user just
   * pressed, so a tap must leave it closed. A tap synthesizes `mouseenter`
   * before `click`; the `click` dismissal clears that pending hover timer,
   * so nothing appears later either.
   */
  describe('touch', () => {
    const tap = (el: Element) => {
      fireEvent.mouseEnter(el)
      fireEvent(el, new PointerEvent('pointerup', { pointerType: 'touch', bubbles: true }))
      fireEvent.click(el)
    }

    it('does not open on a tap', () => {
      render(() => (
        <Tooltip text="Tooltip text">
          <button type="button">Trigger</button>
        </Tooltip>
      ))

      tap(screen.getByRole('button', { name: 'Trigger' }))
      vi.advanceTimersByTime(700)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
      expect(screen.getByRole('button', { name: 'Trigger' })).not.toHaveAttribute('aria-describedby')
    })

    it('does not open on a tap even when the label is clipped', () => {
      render(() => (
        <Tooltip text="Tooltip text" showWhen="clipped">
          {/* jsdom doesn't expand the `overflow` shorthand, so set the longhand. */}
          <button type="button" style={{ 'overflow-x': 'hidden', 'overflow-y': 'hidden' }}>
            A very long label that gets cut off
          </button>
        </Tooltip>
      ))

      const button = screen.getByRole('button')
      Object.defineProperty(button, 'scrollWidth', { value: 200, configurable: true })
      Object.defineProperty(button, 'clientWidth', { value: 50, configurable: true })

      tap(button)
      vi.advanceTimersByTime(700)

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })
  })

  it('warns and leaves invalid children unchanged', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { container } = render(() => (
      <Tooltip text="Broken">
        <>
          <span>One</span>
          <span>Two</span>
        </>
      </Tooltip>
    ))

    expect(warn).toHaveBeenCalled()
    expect(container.textContent).toContain('OneTwo')
    expect(screen.queryByRole('tooltip')).toBeNull()
  })

  it('does not present while a long-press menu is up', () => {
    vi.spyOn(gesture, 'holdIsOverMenu').mockReturnValue(true)

    render(() => (
      <Tooltip text="Tooltip text">
        <button type="button">Trigger</button>
      </Tooltip>
    ))

    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Trigger' }))
    vi.advanceTimersByTime(700)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  it('does not present when a hold starts after the delay is armed', () => {
    const hold = vi.spyOn(gesture, 'holdIsOverMenu').mockReturnValue(false)

    render(() => (
      <Tooltip text="Tooltip text">
        <button type="button">Trigger</button>
      </Tooltip>
    ))

    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Trigger' }))
    hold.mockReturnValue(true)
    vi.advanceTimersByTime(700)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  it('hides a visible tooltip when dismissActiveTooltip runs', () => {
    render(() => (
      <Tooltip text="Tooltip text">
        <button type="button">Trigger</button>
      </Tooltip>
    ))

    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Trigger' }))
    vi.advanceTimersByTime(700)
    expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()

    dismissActiveTooltip()
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })
})

/**
 * A disabled control, which is the case `title` used to serve.
 *
 * A `title` long enough to state a reason BECOMES the control's accessible
 * name, so a screen reader announced the remedy where the label belongs and
 * every by-name lookup stopped matching. The tooltip has to cover the case
 * itself, or the ban on `title` in `eslint.config.ts` takes away the only
 * route these controls had.
 */
describe('tooltip on a disabled control', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  const wrapperOf = (el: Element): HTMLElement => el.parentElement!

  /**
   * Let the MutationObserver deliver.
   *
   * The initial state is read synchronously when the target resolves, so only
   * a CHANGE lags -- by one microtask, which no pointer can beat.
   */
  const flushAttributeChange = () => Promise.resolve()

  it('leaves the control its own accessible name', () => {
    render(() => (
      <Tooltip text="Open the hub over HTTPS to add a passkey.">
        <button type="button" disabled>Add passkey</button>
      </Tooltip>
    ))

    // The name is the LABEL. This is the whole defect: with the reason on
    // `title`, this lookup found nothing and the name was the reason.
    const button = screen.getByRole('button', { name: 'Add passkey' })
    expect(button).not.toHaveAttribute('title')
    expect(button).not.toHaveAttribute('aria-label')
  })

  // A disabled control takes no focus, so `focusin` never fires and the
  // tooltip can only ever open under a pointer. The description is the only
  // route a screen-reader user has, so it does not wait for the tooltip.
  it('describes the control for as long as it is disabled', () => {
    render(() => (
      <Tooltip text="Open the hub over HTTPS to add a passkey.">
        <button type="button" disabled>Add passkey</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Add passkey' })
    const describedBy = button.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const description = document.getElementById(describedBy!)
    expect(description?.textContent).toBe('Open the hub over HTTPS to add a passkey.')
  })

  // A dialog that greys out a destructive action states WHY in its body, and
  // wraps the control so a reader who hovers learns it too. Without
  // `describedBy` the same sentence reaches the accessibility tree twice, and
  // any locator that matches on the text resolves to two elements.
  it('points at the caller element and renders no copy of its own', () => {
    const reason = 'This worktree is locked (held by the e2e test).'
    render(() => (
      <div>
        <div id="visible-reason">{reason}</div>
        <Tooltip text={reason} describedBy="visible-reason">
          <button type="button" disabled>Delete</button>
        </Tooltip>
      </div>
    ))

    const button = screen.getByRole('button', { name: 'Delete' })
    expect(button.getAttribute('aria-describedby')).toBe('visible-reason')

    // ONE node carries the sentence: the one the caller already renders.
    const carrying = Array.from(document.querySelectorAll('*'))
      .filter(el => el.children.length === 0 && el.textContent === reason)
    expect(carrying).toHaveLength(1)
    expect(carrying[0]!.id).toBe('visible-reason')
  })

  // The wrapper is `display: contents` everywhere else, which puts it outside
  // the box tree and therefore outside the hit test -- so it would never see
  // the pointer that the disabled child itself refuses.
  it('gives the wrapper a box, and takes it back when the control is enabled', async () => {
    const [disabled, setDisabled] = createSignal(true)
    render(() => (
      <Tooltip text="Busy">
        <button type="button" disabled={disabled()}>Save</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Save' })
    expect(wrapperOf(button).style.display).toBe('inline-flex')

    // Reactive, because `disabled` moves while the tooltip stays mounted: a
    // button is disabled only while its request is in flight. A read at mount
    // would leave the wrapper boxless for a control that became disabled a
    // tick later.
    setDisabled(false)
    await flushAttributeChange()
    expect(wrapperOf(button).style.display).toBe('contents')
    expect(button).not.toHaveAttribute('aria-describedby')

    setDisabled(true)
    await flushAttributeChange()
    expect(wrapperOf(button).style.display).toBe('inline-flex')
    expect(button.getAttribute('aria-describedby')).toBeTruthy()
  })

  it('opens from the wrapper, which is what the pointer can reach', () => {
    render(() => (
      <Tooltip text="Open the hub over HTTPS to add a passkey.">
        <button type="button" disabled>Add passkey</button>
      </Tooltip>
    ))

    const wrapper = wrapperOf(screen.getByRole('button', { name: 'Add passkey' }))
    fireEvent.mouseEnter(wrapper)
    vi.advanceTimersByTime(700)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Open the hub over HTTPS')

    fireEvent.mouseLeave(wrapper)
    vi.advanceTimersByTime(100)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  // `aria-disabled` takes pointer events of its own, so it needs no wrapper
  // box -- but it still reads as unavailable, so the component still owes it
  // the reason.
  it('describes an aria-disabled control too', () => {
    render(() => (
      <Tooltip text="Not while the worktree is dirty.">
        <button type="button" aria-disabled="true">Switch branch</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Switch branch' })
    expect(button.getAttribute('aria-describedby')).toBeTruthy()
  })

  // And it gets NO box. The wider predicate drove the wrapper too, so an
  // `aria-disabled` control -- which dispatches its own pointer events and
  // needs no wrapper at all -- put an inert inline-flex box into whatever row
  // it sat in.
  it('leaves an aria-disabled control boxless', () => {
    render(() => (
      <Tooltip text="Not while the worktree is dirty.">
        <button type="button" aria-disabled="true">Switch branch</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Switch branch' })
    expect(wrapperOf(button).style.display).toBe('contents')
  })

  // An EMPTY tooltip has nothing a hover could open, so a disabled control
  // under one needs no box either. Two dialog footers ship exactly this shape:
  // a ConfirmButton wrapped in `<Tooltip text={reason() || undefined}>`, where
  // the button is also disabled for unrelated reasons -- an in-flight submit,
  // a repository state that is still loading. Without this the footer's flex
  // row gains an inert box at rest and again for the whole submit.
  it('leaves the wrapper boxless when there is nothing to show', async () => {
    const [reason, setReason] = createSignal<string | undefined>(undefined)
    render(() => (
      <Tooltip text={reason()}>
        <button type="button" disabled>Delete</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Delete' })
    expect(wrapperOf(button).style.display).toBe('contents')
    expect(button).not.toHaveAttribute('aria-describedby')

    // And it takes the box the moment there IS something to show.
    setReason('This branch is checked out.')
    await flushAttributeChange()
    expect(wrapperOf(button).style.display).toBe('inline-flex')
    expect(button.getAttribute('aria-describedby')).toBeTruthy()
  })

  // Per INSTANCE, and the instance count is not limited by the call sites:
  // ClippedText, RelativeTime and IconButton each render a Tooltip
  // unconditionally, so a busy chat screen holds hundreds and a virtualized
  // list allocates and disconnects one for each recycled row on the scroll
  // path.
  it('installs no MutationObserver when there is nothing to show', () => {
    const RealObserver = globalThis.MutationObserver
    const constructed = vi.fn()
    class CountingObserver extends RealObserver {
      constructor(callback: MutationCallback) {
        super(callback)
        constructed()
      }
    }
    vi.stubGlobal('MutationObserver', CountingObserver)
    try {
      render(() => (
        <Tooltip>
          <button type="button" disabled>Delete</button>
        </Tooltip>
      ))
      expect(constructed).not.toHaveBeenCalled()

      render(() => (
        <Tooltip text="This branch is checked out.">
          <button type="button" disabled>Remove</button>
        </Tooltip>
      ))
      expect(constructed).toHaveBeenCalledTimes(1)
    }
    finally {
      vi.stubGlobal('MutationObserver', RealObserver)
    }
  })

  // The listeners sit on the wrapper AND on the target at once, so a disabled
  // flip cannot re-host them. Choosing one host made the disabled state a
  // dependency of that effect: a flip while the pointer already rested on the
  // control moved the listeners with no new `mouseenter` behind them, and that
  // hover's tooltip never opened.
  it('still opens from the target while the control is enabled', () => {
    render(() => (
      <Tooltip text="Save the draft.">
        <button type="button">Save</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Save' })
    fireEvent.mouseEnter(button)
    vi.advanceTimersByTime(700)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Save the draft.')
  })

  it('opens from the wrapper after the control becomes disabled under the pointer', async () => {
    const [disabled, setDisabled] = createSignal(false)
    render(() => (
      <Tooltip text="Saving...">
        <button type="button" disabled={disabled()}>Save</button>
      </Tooltip>
    ))

    const button = screen.getByRole('button', { name: 'Save' })
    setDisabled(true)
    await flushAttributeChange()

    fireEvent.mouseEnter(wrapperOf(button))
    vi.advanceTimersByTime(700)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Saving...')
  })
})
