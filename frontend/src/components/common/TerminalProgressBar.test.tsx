import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { TerminalProgress_State } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TerminalProgressBar } from './TerminalProgressBar'
import { SHOW_DELAY_MS } from './Tooltip'

function terminalTab(progressPercent: number, progressState: TerminalProgress_State): Tab {
  return {
    type: TabType.TERMINAL,
    id: 't-1',
    progressPercent,
    progressState,
  } as Tab
}

describe('TerminalProgressBar', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('fills to the percent and states it to a screen reader', () => {
    render(() => <TerminalProgressBar tab={terminalTab(42, TerminalProgress_State.NORMAL)} />)

    const bar = screen.getByRole('progressbar', { name: 'Terminal progress' })
    expect(bar).toHaveAttribute('data-testid', 'tab-progress')
    expect(bar).toHaveAttribute('aria-valuenow', '42')
    expect(bar).toHaveAttribute('aria-valuemin', '0')
    expect(bar).toHaveAttribute('aria-valuemax', '100')
    expect(bar).toHaveAttribute('aria-valuetext', '42%')
    expect(bar.style.getPropertyValue('--progress-percent')).toBe('42%')
  })

  // AGENTS.md: hover text uses <Tooltip>, never a bare title.
  it('states its value in a tooltip, not in a title', () => {
    render(() => <TerminalProgressBar tab={terminalTab(42, TerminalProgress_State.NORMAL)} />)

    const bar = screen.getByTestId('tab-progress')
    expect(bar).not.toHaveAttribute('title')
    fireEvent.mouseEnter(bar)
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('42%')
  })

  // A terminal reports its progress again and again; the bar follows each
  // report in place, into and out of an indeterminate state.
  it('follows a progress that changes in place', () => {
    const [tab, setTab] = createSignal(terminalTab(10, TerminalProgress_State.NORMAL))
    render(() => <TerminalProgressBar tab={tab()} />)
    const bar = screen.getByRole('progressbar')

    setTab(terminalTab(75, TerminalProgress_State.NORMAL))
    expect(bar).toHaveAttribute('aria-valuenow', '75')
    expect(bar.style.getPropertyValue('--progress-percent')).toBe('75%')

    setTab(terminalTab(75, TerminalProgress_State.INDETERMINATE))
    expect(bar).not.toHaveAttribute('aria-valuenow')
    expect(bar).toHaveAttribute('aria-valuetext', 'In progress')

    setTab(terminalTab(90, TerminalProgress_State.NORMAL))
    expect(bar).toHaveAttribute('aria-valuenow', '90')
    expect(bar).toHaveAttribute('aria-valuetext', '90%')
  })

  // An indeterminate progress has no value: ARIA leaves aria-valuenow out.
  it('states work of no known percent as in progress', () => {
    render(() => <TerminalProgressBar tab={terminalTab(40, TerminalProgress_State.INDETERMINATE)} />)

    const bar = screen.getByRole('progressbar')
    expect(bar).not.toHaveAttribute('aria-valuenow')
    expect(bar).toHaveAttribute('aria-valuetext', 'In progress')
    fireEvent.mouseEnter(bar)
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('In progress')
  })
})
