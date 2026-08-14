import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { attachContextMenuGesture } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { Tooltip } from './Tooltip'

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
   * A tap must reach a clipped label's tooltip. Hover cannot: a tap synthesizes
   * `mouseenter` and then `click`, and the `click` handler dismisses long
   * before the 700 ms hover timer, so nothing ever appeared on a touch screen.
   */
  describe('touch', () => {
    const tap = (el: Element) => {
      fireEvent(el, new PointerEvent('pointerup', { pointerType: 'touch', bubbles: true }))
      fireEvent.click(el)
    }

    it('opens on a tap, with no hover delay', () => {
      render(() => (
        <Tooltip text="Tooltip text">
          <button type="button">Trigger</button>
        </Tooltip>
      ))

      tap(screen.getByRole('button', { name: 'Trigger' }))

      // No timer advance: a tap is deliberate and needs no delay to prove it.
      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
    })

    it('closes on the next press outside the trigger', () => {
      render(() => (
        <Tooltip text="Tooltip text">
          <button type="button">Trigger</button>
        </Tooltip>
      ))

      const button = screen.getByRole('button', { name: 'Trigger' })
      tap(button)
      expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()

      // The trigger's rect is 0x0 in jsdom, so any coordinate is outside it.
      fireEvent(document, new PointerEvent('pointerdown', { clientX: 500, clientY: 500, bubbles: true }))

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })

    it('stays closed on a tap when the label is not clipped', () => {
      render(() => (
        <Tooltip text="Tooltip text" showWhen="clipped">
          <button type="button">Trigger</button>
        </Tooltip>
      ))

      // A mouse click dismisses; a tap that opens nothing must do the same, so
      // the swallow-the-click guard must not latch on a suppressed tooltip.
      tap(screen.getByRole('button', { name: 'Trigger' }))

      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    })

    it('does not present on the release of a long press whose menu is opening', () => {
      // The gesture must let that release propagate (the drag sensor and the
      // chat scroller consume it), so it flags it and the tooltip stands down:
      // a `popover="manual"` tooltip entering the top layer a frame after the
      // menu would stack above it.
      const row = document.createElement('div')
      document.body.appendChild(row)
      const detach = attachContextMenuGesture(row, { onOpen: () => {} })
      try {
        row.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', pointerId: 1, isPrimary: true, bubbles: true, cancelable: true }))
        vi.advanceTimersByTime(motion.longPress)
        row.dispatchEvent(new PointerEvent('pointerup', { pointerType: 'touch', pointerId: 1, bubbles: true, cancelable: true }))

        render(() => (
          <Tooltip text="Tooltip text">
            <button type="button">Trigger</button>
          </Tooltip>
        ))

        const button = screen.getByRole('button', { name: 'Trigger' })
        fireEvent(button, new PointerEvent('pointerup', { pointerType: 'touch', bubbles: true }))
        expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()

        // The menu's open task clears the flag; a tap after it presents as usual.
        vi.runAllTimers()
        tap(button)
        expect(screen.getByRole('tooltip', { hidden: true })).toBeInTheDocument()
      }
      finally {
        detach()
        row.remove()
      }
    })

    it('leaves a mouse click dismissing the tooltip', () => {
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
})
