import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { SetGoalDialog } from './SetGoalDialog'

describe('setGoalDialog', () => {
  // Replacing prefills the current objective, so the reader EDITS what is there
  // instead of retyping it. It is also what makes Clear safe without a
  // confirmation: the editor reopens holding the objective that was cleared.
  it('starts from the objective it was given', () => {
    const { getByTestId } = render(() => (
      <SetGoalDialog initialObjective="every test passes" onSubmit={vi.fn()} onClose={vi.fn()} />
    ))
    expect((getByTestId('set-goal-input') as HTMLTextAreaElement).value).toBe('every test passes')
  })

  it('submits the trimmed objective and closes', () => {
    const onSubmit = vi.fn()
    const onClose = vi.fn()
    const { getByTestId } = render(() => (
      <SetGoalDialog onSubmit={onSubmit} onClose={onClose} />
    ))
    const input = getByTestId('set-goal-input') as HTMLTextAreaElement
    fireEvent.input(input, { target: { value: '  ship it  ' } })
    fireEvent.click(getByTestId('set-goal-submit'))
    expect(onSubmit).toHaveBeenCalledWith('ship it')
    expect(onClose).toHaveBeenCalled()
  })

  it('refuses an objective that is only whitespace', () => {
    const onSubmit = vi.fn()
    const { getByTestId } = render(() => (
      <SetGoalDialog onSubmit={onSubmit} onClose={vi.fn()} />
    ))
    fireEvent.input(getByTestId('set-goal-input'), { target: { value: '   ' } })
    const submit = getByTestId('set-goal-submit') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.click(submit)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('writes nothing when cancelled', () => {
    const onSubmit = vi.fn()
    const onClose = vi.fn()
    const { getByText, getByTestId } = render(() => (
      <SetGoalDialog onSubmit={onSubmit} onClose={onClose} />
    ))
    fireEvent.input(getByTestId('set-goal-input'), { target: { value: 'abandoned' } })
    fireEvent.click(getByText('Cancel'))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })
})
