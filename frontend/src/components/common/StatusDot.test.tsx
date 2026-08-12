import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { statusDot } from '~/styles/shared.css'
import { hoverForTooltip } from '~/test-support/clipStub'
import { StatusDot } from './StatusDot'

describe('statusDot', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  function dotOf(container: HTMLElement): HTMLElement {
    return container.querySelector<HTMLElement>('[role="img"]')!
  }

  // The state is carried by the dot's COLOUR, which reaches nobody who cannot
  // see it. `role="img"` is what lets `aria-label` attach at all: the attribute
  // is prohibited on an element with no role.
  it('names the state for a reader who cannot see the colour', () => {
    const { container } = render(() => <StatusDot label="Connected" />)

    const dot = dotOf(container)
    expect(dot.getAttribute('role')).toBe('img')
    expect(dot.getAttribute('aria-label')).toBe('Connected')
  })

  it('carries the shared shape plus the palette class', () => {
    const { container } = render(() => <StatusDot label="Failed" class="danger" />)

    const classes = dotOf(container).className.trim().split(/\s+/)
    expect(classes).toContain(statusDot)
    expect(classes).toContain('danger')
  })

  it('applies status and testId to the dot itself', () => {
    render(() => <StatusDot label="Connected" status="connected" testId="worker-dot" />)

    const dot = screen.getByTestId('worker-dot')
    expect(dot.getAttribute('data-status')).toBe('connected')
  })

  it('shows the label on hover when a tooltip is asked for', () => {
    const { container } = render(() => <StatusDot label="Running" tooltip />)

    expect(hoverForTooltip(dotOf(container))?.textContent).toBe('Running')
  })

  // A dot inside `actionSlot` is `pointer-events: none`, so a tooltip there
  // could never fire. Leaving it out keeps the accessible name and drops the
  // machinery that would do nothing.
  it('renders no tooltip by default', () => {
    const { container } = render(() => <StatusDot label="Connected" />)

    expect(hoverForTooltip(dotOf(container))).toBeNull()
  })

  it('renders exactly one element, as Tooltip requires of its child', () => {
    const { container } = render(() => <StatusDot label="Running" tooltip />)

    expect(container.firstElementChild!.childElementCount).toBe(1)
  })
})
