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

  it('adds no aria-label for a button whose text is already its name', () => {
    render(() => (
      <ConfirmButton onClick={() => {}}>
        Delete
      </ConfirmButton>
    ))
    expect(screen.getByRole('button')).not.toHaveAttribute('aria-label')
  })

  it('gives an icon-only button its name from the tooltip props, and changes it when armed', () => {
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

  it('describes a blocked button from the element that already shows the reason', () => {
    // LastTabCloseDialog and DeleteBranchDialog say why Delete is refused. They
    // pass `blocked` rather than wrapping this button, because this component
    // owns its own tooltip and a nested one disables the outer one entirely.
    // `reasonId` points at the sentence the dialog ALREADY renders, so the
    // reason reaches the accessibility tree once rather than twice.
    render(() => (
      <>
        <span id="reason-1">held for review</span>
        <ConfirmButton disabled blocked={{ reason: 'held for review', reasonId: 'reason-1' }} onClick={() => {}}>
          Delete worktree
        </ConfirmButton>
      </>
    ))
    const button = screen.getByRole('button')
    expect(button).toHaveAttribute('aria-describedby', 'reason-1')
    // The reason is a DESCRIPTION and never the NAME. A sentence long enough to
    // be useful would replace "Delete worktree" if it went to `aria-label`,
    // which is exactly what `title` is banned for.
    expect(button).not.toHaveAttribute('aria-label')
    expect(button).toHaveAccessibleName('Delete worktree')
  })

  it('renders one tooltip and no second copy of the reason', () => {
    // A caller that wrapped this button instead would nest one `<Tooltip>` in
    // another. That does not merely publish the reason twice: the outer tooltip
    // resolves the INNER wrapper span as its target, stops detecting the
    // disabled state, and once the inner one adds an offscreen description the
    // outer wrapper holds two children and gives up entirely.
    const { container } = render(() => (
      <>
        <span id="reason-2">held for review</span>
        <ConfirmButton disabled blocked={{ reason: 'held for review', reasonId: 'reason-2' }} onClick={() => {}}>
          Delete worktree
        </ConfirmButton>
      </>
    ))
    const copies = [...container.querySelectorAll('*')].filter(el => el.textContent === 'held for review')
    expect(copies).toHaveLength(1)
    expect(copies[0]).toHaveAttribute('id', 'reason-2')
  })

  it('adds no accessible name for a text button that passes no tooltip', () => {
    // The unconditional `<Tooltip>` must stay invisible to a button that never
    // asked for one: with no `tooltip` and no `blocked` it has nothing to show,
    // so it writes no `aria-label` and the children keep naming the button.
    render(() => (
      <ConfirmButton onClick={() => {}}>Close anyway</ConfirmButton>
    ))
    const button = screen.getByRole('button')
    expect(button).not.toHaveAttribute('aria-label')
    expect(button).not.toHaveAttribute('aria-describedby')
    expect(button).toHaveAccessibleName('Close anyway')
  })

  it('keeps the resting name when armed and no confirm tooltip is given', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} tooltip="Delete">
        <svg />
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    // The name does not change, but the button still HAS one. Falling through to undefined would strip
    // the aria-label and leave an icon-only button unlabelled while armed.
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveAttribute('data-armed')
  })

  it('renders an element confirmLabel as an ELEMENT, not as its text', () => {
    render(() => (
      <ConfirmButton onClick={() => {}} confirmLabel={<span data-testid="armed-label">Gone?</span>}>
        Delete
      </ConfirmButton>
    ))
    fireEvent.click(screen.getByRole('button'))
    // The element itself, not `toHaveTextContent`. `confirmLabel` used to take a
    // string alone, and a text assertion passes either way -- Solid's `insert`
    // renders a `<span>` the same before and after the type widened, so it can
    // never observe the change it exists for. An icon-only caller needs the
    // NODE to survive, which this asserts.
    expect(screen.getByTestId('armed-label').tagName).toBe('SPAN')
  })
})
