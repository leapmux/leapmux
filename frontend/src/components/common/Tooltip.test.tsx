import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
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
