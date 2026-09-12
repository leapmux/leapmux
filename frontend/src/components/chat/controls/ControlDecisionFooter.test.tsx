import { fireEvent, render, screen } from '@solidjs/testing-library'
import { Minus } from 'lucide-solid'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { ControlDecisionFooter } from './ControlDecisionFooter'

describe('controlDecisionFooter', () => {
  it('keeps additional decisions in an accessible menu', () => {
    const onSelect = vi.fn()
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        positiveAction={{ label: 'Approve', testId: 'approve', onSelect: vi.fn() }}
        additionalActions={() => [{ label: 'Export plan', testId: 'export-plan', onSelect }]}
      />
    ))
    const trigger = screen.getByRole('button', { name: 'More actions' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Export plan', hidden: true }))
    expect(onSelect).toHaveBeenCalledOnce()
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })

  it('keeps switch focus when its checked state changes', () => {
    const [checked, setChecked] = createSignal(false)
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        switches={() => [{
          id: 'clear-context',
          label: 'Clear Context',
          checked: checked(),
          onChange: setChecked,
        }]}
      />
    ))

    const input = screen.getByTestId('clear-context').querySelector('input')!
    input.focus()
    fireEvent.click(input)

    expect(input.checked).toBe(true)
    expect(screen.getByTestId('clear-context').querySelector('input')).toBe(input)
    expect(document.activeElement).toBe(input)
  })

  it('keeps primary decisions compact and puts additional decisions in the menu', () => {
    // Switches, pill groups, and decision buttons use the same compact size.
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        additionalActions={() => [{ label: 'Allow all', testId: 'allow-all', onSelect: vi.fn() }]}
      />
    ))

    // The negative action is always an outline; the positive one is filled.
    expect(screen.getByTestId('deny')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('allow')).toHaveClass(compactControl)
    expect(screen.getByTestId('allow')).not.toHaveClass('outline')
    const more = screen.getByTestId('control-more-actions')
    expect(more).toHaveAccessibleName('More actions')
    expect(more).toHaveAttribute('aria-expanded', 'false')
    expect(more.compareDocumentPosition(screen.getByTestId('deny')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getByTestId('deny').compareDocumentPosition(screen.getByTestId('allow')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
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

  it('renders provider allow choices in the leading controls', () => {
    const onSelect = vi.fn()
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        allowChoicePill={() => ({
          label: 'Allow as',
          options: [
            { key: 'once', label: 'Once' },
            { key: 'session', label: 'Session' },
          ],
          selected: 'once',
          onSelect,
        })}
      />
    ))

    fireEvent.click(screen.getByRole('radio', { name: 'Session' }))
    expect(onSelect).toHaveBeenCalledWith('session')
  })

  // Request choices precede session permissions in every provider's approval row.
  it('renders the leading cluster in one order', () => {
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: vi.fn() }}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        switches={() => [{ id: 'clear-context', label: 'Clear Context', checked: false, onChange: vi.fn() }]}
        allowChoicePill={() => ({
          label: 'Allow as',
          options: [{ key: 'once', label: 'Once' }, { key: 'session', label: 'Session' }],
          selected: 'once',
          onSelect: vi.fn(),
        })}
        permissionPill={() => ({
          options: [{ key: 'unspecified', label: 'Unchanged' }, { key: 'bypass', label: 'Bypass' }],
          selected: 'unspecified',
          onSelect: vi.fn(),
        })}
      />
    ))

    const switchEl = screen.getByTestId('clear-context')
    const allowGroup = screen.getByTestId('control-allow-choice-pill-group')
    const permissionGroup = screen.getByTestId('control-permissions-pill-group')
    expect(switchEl.compareDocumentPosition(allowGroup) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(allowGroup.compareDocumentPosition(permissionGroup) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
