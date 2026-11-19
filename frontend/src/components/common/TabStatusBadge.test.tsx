import type { TabStatusView } from '~/lib/tabStatus'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabStatusBadge } from './TabStatusBadge'
import { SHOW_DELAY_MS } from './Tooltip'

describe('TabStatusBadge', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('draws the text in the tone, with a ring that states the progress', () => {
    render(() => <TabStatusBadge status={{ text: 'Migrating rows', progressPercent: 40, tone: 'warning' }} />)

    const badge = screen.getByTestId('tab-status')
    expect(badge).toHaveAttribute('data-tone', 'warning')
    expect(screen.getByTestId('tab-status-text')).toHaveTextContent('Migrating rows')
    const ring = screen.getByRole('progressbar', { name: 'Migrating rows' })
    expect(ring).toHaveAttribute('aria-valuenow', '40')
    expect(ring).toHaveAttribute('aria-valuemin', '0')
    expect(ring).toHaveAttribute('aria-valuemax', '100')
    expect(ring.style.getPropertyValue('--tab-status-progress')).toBe('40%')
  })

  // 0 is a progress: an empty ring, not the dot of a status without one.
  it('draws an empty ring for a progress of 0', () => {
    render(() => <TabStatusBadge status={{ text: 'Starting', progressPercent: 0, tone: 'neutral' }} />)

    const ring = screen.getByRole('progressbar')
    expect(ring).toHaveAttribute('aria-valuenow', '0')
    expect(ring.style.getPropertyValue('--tab-status-progress')).toBe('0%')
  })

  // The dot still shows that the tab has a status where a compact tile hides
  // the text.
  it('draws a dot and no ring without a progress', () => {
    render(() => <TabStatusBadge status={{ text: 'Waiting', progressPercent: undefined, tone: 'neutral' }} />)

    expect(screen.queryByRole('progressbar')).toBeNull()
    const badge = screen.getByTestId('tab-status')
    expect(badge).toHaveAttribute('data-tone', 'neutral')
    expect(badge.querySelectorAll('[aria-hidden="true"]')).toHaveLength(1)
  })

  it('draws no dot beside a ring', () => {
    render(() => <TabStatusBadge status={{ text: 'Migrating rows', progressPercent: 40, tone: 'neutral' }} />)

    expect(screen.getByTestId('tab-status').querySelector('[aria-hidden="true"]')).toBeNull()
  })

  it('states the text alone in its tooltip without a progress', () => {
    render(() => <TabStatusBadge status={{ text: 'Waiting for review', progressPercent: undefined, tone: 'neutral' }} />)

    fireEvent.mouseEnter(screen.getByTestId('tab-status'))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true }).textContent).toBe('Waiting for review')
  })

  // The text may be cut, and the percentage is in no text at all, so the
  // tooltip shows both even when the text fits.
  it('states the whole text and the progress in its tooltip', () => {
    render(() => <TabStatusBadge status={{ text: 'Running the integration tests', progressPercent: 75, tone: 'success' }} />)

    fireEvent.mouseEnter(screen.getByTestId('tab-status'))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Running the integration tests (75%)')
  })

  it('follows a status that changes in place', () => {
    const [status, setStatus] = createSignal<TabStatusView>({ text: 'Building', progressPercent: 10, tone: 'neutral' })
    render(() => <TabStatusBadge status={status()} />)

    setStatus({ text: 'Build failed', progressPercent: undefined, tone: 'error' })
    expect(screen.getByTestId('tab-status-text')).toHaveTextContent('Build failed')
    expect(screen.getByTestId('tab-status')).toHaveAttribute('data-tone', 'error')
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('draws a ring in place of the dot when a progress arrives, and follows it', () => {
    const [status, setStatus] = createSignal<TabStatusView>({ text: 'Queued', progressPercent: undefined, tone: 'neutral' })
    render(() => <TabStatusBadge status={status()} />)
    expect(screen.queryByRole('progressbar')).toBeNull()

    setStatus({ text: 'Migrating rows', progressPercent: 10, tone: 'neutral' })
    const ring = screen.getByRole('progressbar', { name: 'Migrating rows' })
    expect(screen.getByTestId('tab-status').querySelector('[aria-hidden="true"]')).toBeNull()

    setStatus({ text: 'Migrating rows', progressPercent: 90, tone: 'neutral' })
    expect(ring).toHaveAttribute('aria-valuenow', '90')
    expect(ring.style.getPropertyValue('--tab-status-progress')).toBe('90%')
  })

  it('states the new text and progress in its tooltip after a change', () => {
    const [status, setStatus] = createSignal<TabStatusView>({ text: 'Building', progressPercent: 10, tone: 'neutral' })
    render(() => <TabStatusBadge status={status()} />)

    setStatus({ text: 'Linking', progressPercent: 95, tone: 'neutral' })
    fireEvent.mouseEnter(screen.getByTestId('tab-status'))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true }).textContent).toBe('Linking (95%)')
  })
})
