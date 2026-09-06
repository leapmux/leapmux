import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Tooltip } from '~/components/common/Tooltip'

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

  it('adds no aria-label for a button that names itself with text', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    expect(screen.getByRole('button')).not.toHaveAttribute('aria-label')
  })

  it('names an icon-only button from the tooltip props, and renames it when armed', () => {
    render(() => (
      <ConfirmButton
        onClick={() => {}}
        tooltip="Delete"
        confirmTooltip="Confirm delete?"
        confirmLabel={<svg data-testid="armed-icon" />}
      >
        <svg data-testid="resting-icon" />
      </ConfirmButton>
    ))
    // The name is the ONLY thing an icon-only button says, so the armed state
    // has to reach the accessibility tree through it.
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
    expect(screen.getByTestId('resting-icon')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    expect(screen.getByRole('button', { name: 'Confirm delete?' })).toBeInTheDocument()
    expect(screen.getByTestId('armed-icon')).toBeInTheDocument()
    expect(screen.queryByTestId('resting-icon')).not.toBeInTheDocument()
  })

  it('restores the resting name when the reset timer expires', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} tooltip="Delete" confirmTooltip="Confirm delete?">
        <svg />
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('button', { name: 'Confirm delete?' })).toBeInTheDocument()

    vi.advanceTimersByTime(10_000)

    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
  })

  it('leaves a caller-supplied Tooltip in sole charge of the button', () => {
    // LastTabCloseDialog and DeleteBranchDialog wrap this button in their own
    // `<Tooltip describedBy>` to say why Delete is refused. Both tooltips
    // resolve the same `<button>` as their target and both write its
    // `aria-label` and `aria-describedby`, so this component must add NO
    // tooltip of its own unless the caller asked for one.
    render(() => (
      <>
        <span id="reason-1">held for review</span>
        <Tooltip text="held for review" describedBy="reason-1">
          <ConfirmButton disabled onClick={() => {}}>
            Delete worktree
          </ConfirmButton>
        </Tooltip>
      </>
    ))
    expect(screen.getByRole('button')).toHaveAttribute('aria-describedby', 'reason-1')
  })

  it('keeps the resting name when armed and no confirm tooltip is given', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} tooltip="Delete">
        <svg />
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    // Not renamed, but still NAMED. Falling through to undefined would strip
    // the aria-label and leave an icon-only button unlabelled while armed.
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveAttribute('data-armed')
  })

  it('renders an element confirmLabel instead of the default text', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} confirmLabel={<span>Gone?</span>}>
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('button')).toHaveTextContent('Gone?')
  })
})
