import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ComposerScheduleControl } from './ComposerScheduleControl'

// The popover element APIs come from the shared  stub, which
// also dispatches the  event the menu keys off.

function renderControl(overrides: { value?: string, existingStored?: string } = {}) {
  const onApply = vi.fn()
  render(() => (
    <ComposerScheduleControl
      value={overrides.value ?? ''}
      {...(overrides.existingStored !== undefined ? { existingStored: overrides.existingStored } : {})}
      onApply={onApply}
    />
  ))
  return { onApply }
}

describe('ComposerScheduleControl', () => {
  it('renders an unmarked trigger with no schedule set', () => {
    renderControl()
    const trigger = screen.getByTestId('schedule-send-trigger')
    expect(trigger).not.toHaveAttribute('data-scheduled')
  })

  it('marks the trigger when a schedule is set', () => {
    renderControl({ value: '2030-01-01T18:45' })
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  it('opens with the draft value and applies a future time', () => {
    const { onApply } = renderControl()
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const field = screen.getByTestId('schedule-send-field') as HTMLInputElement
    expect(field.value).toBe('')
    fireEvent.input(field, { target: { value: '2030-06-01T12:00:00.500' } })
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
    expect(onApply).toHaveBeenCalledWith('2030-06-01T12:00:00.500')
  })

  it('loads an existing UTC schedule as a local field value', () => {
    // The stored instant names 09:45:30.123 UTC; the field must show it in the
    // browser's own timezone, not by slicing the string.
    renderControl({ value: '2030-01-01T18:45:30.123', existingStored: '2030-01-01T09:45:30.123Z' })
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const field = screen.getByTestId('schedule-send-field') as HTMLInputElement
    expect(field.value).toBe('2030-01-01T18:45:30.123')
  })

  it('clears the schedule and keeps a past stored deadline valid until changed', () => {
    const { onApply } = renderControl({ value: '2020-01-01T09:00', existingStored: '2020-01-01T00:00:00.000Z' })
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    fireEvent.click(screen.getByTestId('schedule-send-clear'))
    expect(onApply).toHaveBeenCalledWith('')
  })

  it('cancel leaves the draft untouched', () => {
    const { onApply } = renderControl({ value: '2030-01-01T18:45' })
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const field = screen.getByTestId('schedule-send-field') as HTMLInputElement
    fireEvent.input(field, { target: { value: '2031-02-03T04:05' } })
    fireEvent.click(screen.getByTestId('schedule-send-cancel'))
    expect(onApply).not.toHaveBeenCalled()
  })

  it('exposes a refusal through role=alert and disables Schedule', () => {
    renderControl()
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const field = screen.getByTestId('schedule-send-field') as HTMLInputElement
    fireEvent.input(field, { target: { value: '2020-01-01T00:00:00' } })
    const error = screen.getByTestId('schedule-send-error')
    expect(error).toHaveAttribute('role', 'alert')
    expect(screen.getByTestId('schedule-send-apply')).toBeDisabled()
    expect(field).toHaveAttribute('aria-invalid', 'true')
  })
})
