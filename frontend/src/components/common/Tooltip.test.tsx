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
