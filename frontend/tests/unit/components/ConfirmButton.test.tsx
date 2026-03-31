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
    expect(screen.getByRole('button')).toHaveTextContent('Delete')
  })

  it('shows confirm label after first click', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('button')).toHaveTextContent('Confirm?')
  })

  it('shows custom confirm label', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} confirmLabel="Sure?">
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('button')).toHaveTextContent('Sure?')
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
    expect(button).toHaveTextContent('Delete')
  })

  it('resets after 10 seconds of inactivity', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    expect(button).toHaveTextContent('Confirm?')

    vi.advanceTimersByTime(10_000)
    expect(button).toHaveTextContent('Delete')
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
    expect(button).toHaveTextContent('Confirm?')
  })

  it('resets on blur', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    fireEvent.click(button)
    expect(button).toHaveTextContent('Confirm?')

    fireEvent.blur(button)
    vi.runAllTimers()
    expect(button).toHaveTextContent('Delete')
  })

  it('does not swallow a click on a neighboring button after arming', () => {
    const onCancel = vi.fn()
    render(() => (
      <>
        <ConfirmButton onClick={() => {}}>
          Delete
        </ConfirmButton>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </>
    ))

    const [confirmButton, cancelButton] = screen.getAllByRole('button')
    fireEvent.click(confirmButton)
    expect(confirmButton).toHaveTextContent('Confirm?')

    fireEvent.blur(confirmButton)
    fireEvent.click(cancelButton)
    vi.runAllTimers()

    expect(onCancel).toHaveBeenCalledOnce()
    expect(confirmButton).toHaveTextContent('Delete')
  })

  it('sets data-armed attribute when armed', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    const button = screen.getByRole('button')
    expect(button).not.toHaveAttribute('data-armed')

    fireEvent.click(button)
    expect(button).toHaveAttribute('data-armed')
  })
})
