import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConfirmButton } from '~/components/common/ConfirmButton'

describe('confirmButton', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows initial children text', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    expect(screen.getByRole('button').textContent).toBe('Delete')
  })

  it('shows confirm label after first click', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('button').textContent).toBe('Confirm?')
  })

  it('shows custom confirm label', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} confirmLabel="Sure?">
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('button').textContent).toBe('Sure?')
  })

  it('does not call onClick on first click', () => {
    const onClick = vi.fn()
    render(() => (
      <ConfirmButton onClick={onClick}>
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(onClick).not.toHaveBeenCalled()
  })

  it('calls onClick on second click', () => {
    const onClick = vi.fn()
    render(() => (
      <ConfirmButton onClick={onClick}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    fireEvent.click(button)
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('resets to initial state after onClick', () => {
    const onClick = vi.fn()
    render(() => (
      <ConfirmButton onClick={onClick}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    fireEvent.click(button)
    expect(button.textContent).toBe('Delete')
  })

  it('resets after 10 seconds of inactivity', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    expect(button.textContent).toBe('Confirm?')

    vi.advanceTimersByTime(10_000)
    expect(button.textContent).toBe('Delete')
  })

  it('does not reset before 10 seconds', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)

    vi.advanceTimersByTime(9_999)
    expect(button.textContent).toBe('Confirm?')
  })

  it('resets on blur', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    expect(button.textContent).toBe('Confirm?')

    fireEvent.blur(button)
    expect(button.textContent).toBe('Delete')
  })

  it('sets data-armed attribute when armed', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    expect(button.getAttribute('data-armed')).toBeNull()

    fireEvent.click(button)
    expect(button.getAttribute('data-armed')).not.toBeNull()
  })
})
