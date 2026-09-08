import { fireEvent, render, screen } from '@solidjs/testing-library'
import { Minus } from 'lucide-solid'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { SHOW_DELAY_MS } from '~/components/common/Tooltip'
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
    // three is the reason the buttons carry the shared compact style.
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
    expect(screen.getByTestId('deny')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('allow')).toHaveClass(compactControl)
    expect(screen.getByTestId('allow')).not.toHaveClass('outline')
    expect(screen.getByTestId('allow-all')).toHaveClass('outline', compactControl)
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

    expect(screen.getByTestId('deny')).toHaveClass('outline', compactControl)
  })

  it('describes the permission group and gives its icon the specific tooltip', () => {
    vi.useFakeTimers()
    try {
      render(() => (
        <ControlDecisionFooter
          hasEditorContent={false}
          onSendFeedback={vi.fn()}
          negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
          positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
          permissionPill={() => ({
            options: [
              { key: 'unspecified', label: 'Unchanged', icon: Minus },
              { key: 'smart', label: 'Smart' },
            ],
            selected: 'unspecified',
            onSelect: vi.fn(),
          })}
        />
      ))

      const group = screen.getByRole('radiogroup', { name: 'Permissions' })
      expect(group).toHaveAccessibleDescription('The selected preset applies when you allow or approve this request')

      screen.getByRole('radio', { name: 'Unchanged' }).focus()
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Unchanged')
    }
    finally {
      vi.useRealTimers()
    }
  })
})
