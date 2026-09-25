import { fireEvent, render, screen } from '@solidjs/testing-library'
import { Minus } from 'lucide-solid'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { dangerMenuItem } from '~/styles/shared.css'
import { ControlDecisionFooter } from './ControlDecisionFooter'

describe('ControlDecisionFooter', () => {
  it('keeps disabled decisions inactive and restores them when enabled', () => {
    const [disabled, setDisabled] = createSignal(true)
    const select = vi.fn()
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: select, disabled: disabled() }}
        negativeAction={{ label: 'Deny', testId: 'deny', onSelect: select, disabled: disabled() }}
        additionalActions={() => [{ label: 'Cancel', testId: 'cancel', onSelect: select, disabled: disabled() }]}
      />
    ))
    for (const id of ['allow', 'deny']) {
      expect(screen.getByTestId(id)).toBeDisabled()
      screen.getByTestId(id).click()
    }
    fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
    const cancel = screen.getByRole('menuitem', { name: 'Cancel', hidden: true })
    expect(cancel).toBeDisabled()
    cancel.click()
    expect(select).not.toHaveBeenCalled()
    setDisabled(false)
    expect(cancel).toBeEnabled()
    cancel.click()
    expect(select).toHaveBeenCalledOnce()
    expect(screen.getByTestId('allow')).toBeEnabled()
    expect(screen.getByTestId('deny')).toBeEnabled()
  })

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

// A menu item states what it does in its tooltip when its label does not say it all.
// An item with no description gets no tooltip, and a click on either one still selects.
describe('ControlDecisionFooter action descriptions', () => {
  function renderMenu(onSelect = vi.fn()) {
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        positiveAction={{ label: 'Approve', testId: 'approve', onSelect: vi.fn() }}
        additionalActions={() => [
          { label: 'Approve: Option A', testId: 'described', onSelect, description: 'Split the parser first' },
          { label: 'Request revisions', testId: 'plain', onSelect },
        ]}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
  }

  // A keyboard reader reaches the description too: focus opens the tooltip, and the
  // open tooltip describes the item.
  it('describes a focused item that states a description, and only that item', () => {
    vi.useFakeTimers()
    try {
      renderMenu()
      fireEvent.focusIn(screen.getByTestId('described'))
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.getByTestId('described')).toHaveAccessibleDescription('Split the parser first')
      fireEvent.focusOut(screen.getByTestId('described'))
      fireEvent.focusIn(screen.getByTestId('plain'))
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.getByTestId('plain')).not.toHaveAttribute('aria-describedby')
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('shows the description as the tooltip of the item', () => {
    vi.useFakeTimers()
    try {
      renderMenu()
      fireEvent.mouseEnter(screen.getByTestId('described'))
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Split the parser first')
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('shows no tooltip for an item with no description', () => {
    vi.useFakeTimers()
    try {
      renderMenu()
      fireEvent.mouseEnter(screen.getByTestId('plain'))
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('selects a described item on a click, as it selects a plain one', () => {
    const onSelect = vi.fn()
    renderMenu(onSelect)
    fireEvent.click(screen.getByTestId('described'))
    expect(onSelect).toHaveBeenCalledOnce()
  })
})

// "Reject always" and "Allow for this workspace" both land in the overflow menu,
// and an undifferentiated menu made them read as the same kind of answer. See
// REMOVALS-FE-1.
describe('ControlDecisionFooter destructive extras', () => {
  it('marks a refusal in the overflow menu and leaves the others plain', () => {
    render(() => (
      <ControlDecisionFooter
        hasEditorContent={false}
        onSendFeedback={vi.fn()}
        positiveAction={{ label: 'Allow', testId: 'allow', onSelect: vi.fn() }}
        additionalActions={() => [
          { label: 'Reject always', testId: 'reject-always', onSelect: vi.fn(), destructive: true },
          { label: 'Allow for this workspace', testId: 'allow-workspace', onSelect: vi.fn() },
        ]}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
    expect(screen.getByTestId('reject-always')).toHaveClass(dangerMenuItem)
    expect(screen.getByTestId('allow-workspace')).not.toHaveClass(dangerMenuItem)
  })
})
