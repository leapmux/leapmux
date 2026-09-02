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
})
