import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { ControlDecisionFooter } from './ControlDecisionFooter'

describe('controlDecisionFooter', () => {
  it('keeps switch focus when its checked state changes', () => {
    const [checked, setChecked] = createSignal(false)
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        switches={() => [{
          id: 'remember',
          label: 'Remember',
          checked: checked(),
          onChange: setChecked,
        }]}
      />
    ))

    const input = screen.getByTestId('remember').querySelector('input')!
    input.focus()
    fireEvent.click(input)

    expect(input.checked).toBe(true)
    expect(screen.getByTestId('remember').querySelector('input')).toBe(input)
    expect(document.activeElement).toBe(input)
  })

  it('draws every decision at the small row metrics', () => {
    // The row mixes a switch, a pill group and these buttons. One size for all
    // three is the reason the buttons carry Oat's `.small` rather than its
    // default metrics.
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        additionalActions={() => [{ label: 'Allow all', testId: 'allow-all', onSelect: vi.fn(), outline: true }]}
      />
    ))

    // The negative action is always an outline; the positive one is filled.
    expect(screen.getByTestId('deny')).toHaveClass('outline', 'small')
    expect(screen.getByTestId('allow')).toHaveClass('small')
    expect(screen.getByTestId('allow')).not.toHaveClass('outline')
    expect(screen.getByTestId('allow-all')).toHaveClass('outline', 'small')
  })

  it('keeps the feedback action at the same metrics', () => {
    render(() => (
      <ControlDecisionFooter
        hasEditorContent
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('deny')).toHaveClass('outline', 'small')
  })
})
