import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ComposerScheduleControl, SCHEDULE_CLOCK_TICK_MS } from './ComposerScheduleControl'
import { localFieldValueOf } from './scheduleSend'

// The Popover API stubs come from `vitest.setup.ts`, which also dispatches the
// `toggle` event that `DropdownMenu` listens for. Every instant below is built
// with the local Date constructor, so the cases hold in any host timezone.

const NOW = new Date(2030, 0, 1, 12, 0, 0)

beforeEach(() => {
  vi.useFakeTimers({
    toFake: [
      'setTimeout',
      'clearTimeout',
      'setInterval',
      'clearInterval',
      'Date',
    ],
  })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  vi.useRealTimers()
})

function renderControl(overrides: { value?: string, existingStored?: string } = {}) {
  const onApply = vi.fn()
  const result = render(() => (
    <ComposerScheduleControl
      value={overrides.value ?? ''}
      {...(overrides.existingStored !== undefined
        ? { existingStored: overrides.existingStored }
        : {})}
      onApply={onApply}
    />
  ))
  return { onApply, ...result }
}

const fieldInput = () => screen.getByTestId('schedule-send-field') as HTMLInputElement
const open = () => fireEvent.click(screen.getByTestId('schedule-send-trigger'))
const later = (ms: number) => localFieldValueOf(new Date(NOW.getTime() + ms))

describe('ComposerScheduleControl', () => {
  it('renders an unmarked trigger with no schedule set', () => {
    renderControl()
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
  })

  it('marks the trigger when a schedule is set', () => {
    renderControl({ value: '2030-01-01T18:45' })
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  it('renders the popover as a card', () => {
    renderControl()
    expect(screen.getByTestId('schedule-send-popover').classList).toContain('card')
  })

  it('initializes the field before the native popover becomes visible', () => {
    renderControl({ value: '2030-01-01T18:45' })
    const event = new Event('beforetoggle')
    Object.defineProperty(event, 'newState', { value: 'open' })
    fireEvent(screen.getByTestId('schedule-send-popover'), event)
    expect(fieldInput()).toHaveValue('2030-01-01T18:45')
  })

  it('opens with the draft value and applies a future time', () => {
    const { onApply } = renderControl()
    open()
    expect(fieldInput().value).toBe('')
    expect(fieldInput()).toHaveAccessibleDescription('Uses your local timezone.')
    fireEvent.input(fieldInput(), {
      target: { value: '2030-06-01T12:00:00.500' },
    })
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
    expect(onApply).toHaveBeenCalledWith('2030-06-01T12:00:00.500')
  })

  it('applies the whole-minute form that the browser writes when the seconds are zero', () => {
    const { onApply } = renderControl()
    open()
    fireEvent.input(fieldInput(), {
      target: { value: '2030-01-01T18:45' },
    })
    expect(screen.queryByTestId('schedule-send-error')).toBeNull()
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
    expect(onApply).toHaveBeenCalledWith('2030-01-01T18:45')
  })

  it('starts each opening from the draft, so a cancelled value does not come back', () => {
    const { onApply } = renderControl()
    open()
    fireEvent.input(fieldInput(), {
      target: { value: '2031-02-03T04:05' },
    })
    fireEvent.click(screen.getByTestId('schedule-send-cancel'))
    open()
    expect(fieldInput().value).toBe('')
    fireEvent.keyDown(fieldInput(), { key: 'Enter' })
    expect(onApply).toHaveBeenCalledWith('')
    expect(onApply).not.toHaveBeenCalledWith('2031-02-03T04:05')
  })

  it('clears a set schedule', () => {
    const { onApply } = renderControl({ value: '2030-01-01T18:45' })
    open()
    fireEvent.click(screen.getByTestId('schedule-send-clear'))
    expect(onApply).toHaveBeenCalledWith('')
  })

  it('clears a schedule explicitly while the native date input is incomplete', () => {
    const { onApply } = renderControl({ value: '2030-01-01T18:45' })
    open()
    vi.spyOn(fieldInput().validity, 'badInput', 'get').mockReturnValue(true)
    fireEvent.input(fieldInput(), {
      target: { value: '' },
    })
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
    expect(screen.getByTestId('schedule-send-clear')).not.toBeDisabled()
    fireEvent.click(screen.getByTestId('schedule-send-clear'))
    expect(onApply).toHaveBeenCalledExactlyOnceWith('')
    expect(screen.getByTestId('schedule-send-popover')).not.toHaveAttribute('data-popover-open')
  })

  it('disables Clear schedule when no schedule is set', () => {
    renderControl()
    open()
    expect(screen.getByTestId('schedule-send-clear')).toBeDisabled()
  })

  it('keeps an unchanged past stored deadline valid on a queue edit', () => {
    const past = new Date(NOW.getTime() - 60_000)
    const { onApply } = renderControl({
      value: localFieldValueOf(past),
      existingStored: past.toISOString(),
    })
    open()
    expect(screen.queryByTestId('schedule-send-error')).toBeNull()
    expect(fieldInput()).not.toHaveAttribute('aria-invalid')
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
    expect(onApply).toHaveBeenCalledWith(localFieldValueOf(past))
  })

  it('refuses a newly chosen past time on a queue edit', () => {
    const past = new Date(NOW.getTime() - 60_000)
    renderControl({
      value: localFieldValueOf(past),
      existingStored: past.toISOString(),
    })
    open()
    fireEvent.input(fieldInput(), {
      target: {
        value: later(-120_000),
      },
    })
    expect(screen.getByTestId('schedule-send-error')).toHaveTextContent('Pick a time in the future.')
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
  })

  it('exposes a past time through role=alert and disables Schedule', () => {
    renderControl()
    open()
    fireEvent.input(fieldInput(), {
      target: { value: '2020-01-01T00:00' },
    })
    const error = screen.getByTestId('schedule-send-error')
    expect(error).toHaveAttribute('role', 'alert')
    expect(error).toHaveTextContent('Pick a time in the future.')
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
    expect(fieldInput()).toHaveAttribute('aria-invalid', 'true')
    expect(fieldInput()).toHaveAccessibleDescription('Uses your local timezone. Pick a time in the future.')
    fireEvent.input(fieldInput(), {
      target: {
        value: later(60_000),
      },
    })
    expect(screen.queryByTestId('schedule-send-error')).toBeNull()
    expect(fieldInput()).toHaveAccessibleDescription('Uses your local timezone.')
  })

  it('states that a draft with no real local time is invalid', () => {
    // The input sanitizes such a value away, so only the draft can carry one.
    renderControl({ value: '2030-02-30T10:00' })
    open()
    expect(screen.getByTestId('schedule-send-error')).toHaveTextContent('Enter a real local date and time.')
  })

  it('refuses a chosen time once it passes while the popover stays open', () => {
    renderControl()
    open()
    fireEvent.input(fieldInput(), {
      target: {
        value: later(30_000),
      },
    })
    expect(screen.getByTestId('schedule-send-apply')).not.toBeDisabled()
    vi.advanceTimersByTime(40_000)
    expect(screen.getByTestId('schedule-send-error')).toHaveTextContent('Pick a time in the future.')
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
  })

  it('reads the clock again on apply, so a time that passed within one tick is refused with its message', () => {
    const { onApply } = renderControl()
    open()
    fireEvent.input(fieldInput(), {
      target: {
        value: later(500),
      },
    })
    // The wall clock passes the chosen time before the first tick runs.
    vi.setSystemTime(NOW.getTime() + 600)
    expect(600).toBeLessThan(SCHEDULE_CLOCK_TICK_MS)
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
    expect(onApply).not.toHaveBeenCalled()
    expect(screen.getByTestId('schedule-send-error')).toHaveTextContent('Pick a time in the future.')
  })

  it('refuses native partial input, then accepts a corrected date', () => {
    const { onApply } = renderControl({ value: '2030-01-01T18:45' })
    open()
    const validity = vi.spyOn(fieldInput().validity, 'badInput', 'get').mockReturnValue(true)
    fireEvent.input(fieldInput(), {
      target: { value: '' },
    })
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
    fireEvent.keyDown(fieldInput(), { key: 'Enter' })
    expect(onApply).not.toHaveBeenCalled()
    expect(screen.getByTestId('schedule-send-error')).toHaveTextContent('Enter a real local date and time.')
    validity.mockReturnValue(false)
    fireEvent.input(fieldInput(), {
      target: { value: '2030-01-02T18:45' },
    })
    expect(screen.getByTestId('schedule-send-apply')).not.toBeDisabled()
    fireEvent.keyDown(fieldInput(), { key: 'Enter' })
    expect(onApply).toHaveBeenCalledWith('2030-01-02T18:45')
  })

  it('runs its clock only while the popover is open', () => {
    const { unmount } = renderControl()
    const idle = vi.getTimerCount()
    open()
    expect(vi.getTimerCount()).toBe(idle + 1)
    fireEvent.click(screen.getByTestId('schedule-send-cancel'))
    expect(vi.getTimerCount()).toBe(idle)
    open()
    unmount()
    expect(vi.getTimerCount()).toBe(idle)
  })

  it('leaves the arrow keys to the date field', () => {
    renderControl()
    open()
    fieldInput().focus()
    const notCancelled = fireEvent.keyDown(fieldInput(), { key: 'ArrowDown' })
    expect(notCancelled).toBe(true)
    expect(document.activeElement).toBe(fieldInput())
  })

  it('gives each control its own field id, and points its label at it', () => {
    render(() => (
      <>
        <ComposerScheduleControl value="" onApply={() => {}} />
        <ComposerScheduleControl value="" onApply={() => {}} />
      </>
    ))
    const fields = screen.getAllByTestId('schedule-send-field')
    expect(fields[0]!.id).not.toBe(fields[1]!.id)
    for (const field of fields)
      expect(document.querySelector(`label[for="${field.id}"]`)).not.toBeNull()
  })
})
