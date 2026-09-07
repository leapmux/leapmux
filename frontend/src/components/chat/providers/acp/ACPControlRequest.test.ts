import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { ACPControlActions, sendACPPermissionResponse } from './ACPControlRequest'

function decodeOptionId(content: Uint8Array): string {
  return JSON.parse(new TextDecoder().decode(content)).result.outcome.optionId
}

describe('sendACPPermissionResponse', () => {
  it('sends an ACP permission response with the selected option', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    await sendACPPermissionResponse(onRespond, '7', 'proceed_once')

    expect(onRespond).toHaveBeenCalledTimes(1)
    const [content] = onRespond.mock.calls[0]
    const parsed = JSON.parse(new TextDecoder().decode(content))
    expect(parsed).toEqual({
      jsonrpc: '2.0',
      id: 7,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'proceed_once',
        },
      },
    })
  })
})

describe('acpControlActions', () => {
  /** Goose's wire shape: four kind-named options, emitted allow-first. */
  function renderGooseActions() {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()
    render(() => ACPControlActions({
      request: {
        agentId: 'agent1',
        requestId: '7',
        payload: {
          params: {
            options: [
              { optionId: 'allow_always', kind: 'allow_always', name: 'allow_always' },
              { optionId: 'allow_once', kind: 'allow_once', name: 'allow_once' },
              { optionId: 'reject_once', kind: 'reject_once', name: 'reject_once' },
              { optionId: 'reject_always', kind: 'reject_always', name: 'reject_always' },
            ],
          },
        },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
      presets: {
        smart: { sets: { permissionMode: 'smart_approve' } },
        bypass: { sets: { permissionMode: 'auto' } },
        apply,
      },
    }))
    return { onRespond, apply }
  }

  /** OpenCode's wire shape: an allow_always with no reject_always. */
  function renderOpenCodeShapeActions() {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => ACPControlActions({
      request: {
        agentId: 'agent1',
        requestId: '8',
        payload: {
          params: {
            options: [
              { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
              { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
              { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
            ],
          },
        },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
    }))
    return { onRespond }
  }

  it('renders Deny before Allow with Once / Always scope pills and the permission pills, never the raw always buttons', () => {
    renderGooseActions()

    // Negative before positive; the polarity labels are ours, and the duration
    // lives in the scope pills rather than the button text.
    const deny = screen.getByTestId('control-deny-btn')
    const allow = screen.getByTestId('control-allow-btn')
    expect(deny.textContent).toBe('Deny')
    expect(allow.textContent).toBe('Allow')
    expect(deny.compareDocumentPosition(allow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    const scope = within(screen.getByRole('radiogroup', { name: 'Allow scope' }))
    expect(scope.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(scope.getByRole('radio', { name: 'Always' })).not.toBeChecked()
    const pill = within(screen.getByRole('radiogroup', { name: 'Permissions' }))
    expect(pill.getByRole('radio', { name: 'Default' })).toBeChecked()
    // The always options live in the scope group, not as their own buttons.
    expect(screen.queryByTestId('control-decision-allow_always')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-reject_always')).not.toBeInTheDocument()
  })

  it('sends the once options while Once is selected', async () => {
    const { onRespond } = renderGooseActions()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('allow_once')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1][0])).toBe('reject_once')
  })

  it('sends the always options once a scope beyond Once is selected', async () => {
    const { onRespond } = renderGooseActions()

    fireEvent.click(within(screen.getByRole('radiogroup', { name: 'Allow scope' })).getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('allow_always')

    // A remembering scope also upgrades Deny for the one agent offering reject_always.
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1][0])).toBe('reject_always')
  })

  it('keeps the reject-once option under a remembering scope when the agent offers no reject_always', async () => {
    const { onRespond } = renderOpenCodeShapeActions()

    fireEvent.click(within(screen.getByRole('radiogroup', { name: 'Allow scope' })).getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('reject')

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1][0])).toBe('always')
  })

  it('draws no scope group when the agent offers no always option', () => {
    render(() => ACPControlActions({
      request: {
        agentId: 'agent1',
        requestId: '9',
        payload: {
          params: {
            options: [
              { optionId: 'once', kind: 'allow_once', name: 'Allow' },
              { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
            ],
          },
        },
      },
      onRespond: vi.fn().mockResolvedValue(undefined),
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
    }))

    expect(screen.queryByRole('radiogroup', { name: 'Allow scope' })).not.toBeInTheDocument()
  })

  it('applies the chosen permission preset after an allow and never after a reject', async () => {
    const { onRespond, apply } = renderGooseActions()

    fireEvent.click(within(screen.getByRole('radiogroup', { name: 'Permissions' })).getByRole('radio', { name: 'Bypass permissions' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(onRespond).toHaveBeenCalledTimes(1)
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(onRespond).toHaveBeenCalledTimes(2)
    expect(apply).toHaveBeenCalledTimes(1)
  })

  it('draws a Once / Session / Project pill group for Reasonix\'s two always scopes, replacing the Remember switch', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => ACPControlActions({
      request: {
        agentId: 'agent1',
        requestId: '13',
        payload: {
          params: {
            options: [
              { optionId: 'reasonix_write_once', kind: 'allow_once', name: 'Allow once' },
              { optionId: 'reasonix_write_session', kind: 'allow_always', name: 'Allow these directories for this session' },
              { optionId: 'reasonix_write_project', kind: 'allow_always', name: 'Add to project allow_write' },
              { optionId: 'reasonix_write_deny', kind: 'reject_once', name: 'Reject' },
            ],
          },
        },
      },
      onRespond,
      answerState: createControlAnswerState(),
      hasEditorContent: false,
      onTriggerSend: vi.fn(),
    }))

    // The scope group replaces both the Remember switch and the extra button
    // the project option used to be.
    const scope = within(screen.getByRole('radiogroup', { name: 'Allow scope' }))
    expect(scope.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(scope.getByRole('radio', { name: 'Session' })).toBeInTheDocument()
    expect(scope.getByRole('radio', { name: 'Project' })).toBeInTheDocument()
    expect(screen.queryByTestId('control-remember-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-reasonix_write_project')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn').textContent).toBe('Allow')

    // Allow sends the selected scope's option, Once first, Project after a pick.
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0][0])).toBe('reasonix_write_once')

    fireEvent.click(scope.getByRole('radio', { name: 'Project' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1][0])).toBe('reasonix_write_project')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[2][0])).toBe('reasonix_write_deny')
  })
})
