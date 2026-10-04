import type { Mock } from 'vitest'
import type { PlanChoice } from '../model/controlPrompt'
import type { ControlResponseSender } from './types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'
import { dangerMenuItem } from '~/styles/shared.css'
import { permissionPillGroup } from '~/test-support/controlRequests'
import { createControlAnswerState } from './types'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

describe('ExitPlanModeActions', () => {
  it('shows Reject, Approve, the Clear Context switch, and the permission pills when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply: vi.fn(),
        }}
        contextUsage={{ inputTokens: 300, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 }}
        modelContextWindow={1000}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toHaveTextContent('Clear Context (30%)')
    // A plan approval opens on Smart, whatever the session runs on.
    expect(permissionPillGroup().getByRole('radio', { name: 'Smart' })).toBeChecked()
    expect(permissionPillGroup().getByRole('radio', { name: 'Unchanged' })).not.toBeChecked()
    expect(permissionPillGroup().getByRole('radio', { name: 'Bypass' })).not.toBeChecked()
  })

  it('shows only Send feedback when editor has content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-reject-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-clear-context-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
  })

  it('sends clearContext when Clear Context is checked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-clear', 'agent-clear')}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded).not.toHaveProperty('clearContext')
    expect(options.planApproval.clearContext).toBe(true)
    expect(decoded.response.response.behavior).toBe('allow')
  })

  it('sends allow response with the bypass mode when Bypass is selected', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply }}
      />
    ))

    // Select bypass permissions, then approve.
    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-99')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded).not.toHaveProperty('permissionMode')
    expect(options.planApproval.permissionMode).toBe('bypassPermissions')
    // One RPC carries the response and plan settings. A separate settings call could race the restart.
    expect(apply).not.toHaveBeenCalled()
  })

  it('sends allow response with the smart mode when Smart is selected', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply,
        }}
      />
    ))

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Smart' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    expect(JSON.parse(new TextDecoder().decode(bytes))).not.toHaveProperty('permissionMode')
    expect(options.planApproval.permissionMode).toBe('auto')
    expect(apply).not.toHaveBeenCalled()
  })

  it('carries the smart mode when the user touches no pill', () => {
    // Smart is the opening choice, so an approval attaches its mode with no
    // click on the group.
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    expect(JSON.parse(new TextDecoder().decode(bytes))).not.toHaveProperty('permissionMode')
    expect(options.planApproval.permissionMode).toBe('auto')
  })

  it('carries no mode when the catalog offers no smart preset', () => {
    // The opening choice clamps to Unchanged, so an untouched group leaves the
    // agent's permission mode where it is.
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    expect(JSON.parse(new TextDecoder().decode(bytes))).not.toHaveProperty('permissionMode')
    expect(options.planApproval).toEqual({ permissionMode: '', clearContext: false })
  })

  // A preset that switches some axis OTHER than the permission mode cannot act
  // through this banner at all: the approval travels as one control response, and
  // the only part of a preset that response can carry is the mode. Copilot's
  // bypass sets `allow_all`, so its pill is not drawn here -- drawing it would
  // produce a pill that silently did nothing on a plan approval.
  it('draws no pills for presets that carry no permission mode', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-77', 'agent-7')}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { allow_all: 'on' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).toBeNull()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  it('sends allow response without permissionMode for normal approve', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-42', 'agent-5')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes, options] = respondCall ?? []
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded).not.toHaveProperty('permissionMode')
    expect(options.planApproval).toEqual({ permissionMode: '', clearContext: false })
  })

  it('does not show the permission pills when presets are absent', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  describe('with the choices a runtime offers', () => {
    const choices = [
      { id: 'Option A', label: 'Approve: Option A', approves: true },
      { id: 'Revise', label: 'Request revisions', approves: false },
    ]

    function renderWithChoices(onRespond: Mock<ControlResponseSender>, hasEditorContent = false, offered: PlanChoice[] = choices) {
      render(() => (
        <ExitPlanModeActions
          request={makeRequest('req-choice', 'agent-choice')}
          answerState={createControlAnswerState()}
          onRespond={onRespond}
          hasEditorContent={hasEditorContent}
          onTriggerSend={() => {}}
          presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
          choices={offered}
        />
      ))
    }

    function decodeCall(onRespond: Mock<ControlResponseSender>) {
      const call = onRespond.mock.calls[0]
      if (!call)
        throw new Error('the choice sent no response')
      const [bytes, options] = call
      return { decoded: JSON.parse(new TextDecoder().decode(bytes)), options }
    }

    // A runtime can state what each approach does. The reader needs it to choose,
    // because the plan text need not repeat it.
    it('states what each approach does in the tooltip of its menu item', () => {
      vi.useFakeTimers()
      try {
        renderWithChoices(vi.fn<ControlResponseSender>().mockResolvedValue(undefined), false, [
          { id: 'Option A', label: 'Approve: Option A', description: 'Split the parser first', approves: true },
          { id: 'Revise', label: 'Request revisions', approves: false },
        ])
        fireEvent.click(screen.getByTestId('control-more-actions'))
        fireEvent.mouseEnter(screen.getByTestId('plan-choice-0'))
        vi.advanceTimersByTime(700)
        expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('Split the parser first')
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('offers each choice in the overflow menu', () => {
      renderWithChoices(vi.fn<ControlResponseSender>().mockResolvedValue(undefined))
      fireEvent.click(screen.getByTestId('control-more-actions'))
      expect(screen.getByTestId('plan-choice-0')).toHaveTextContent('Approve: Option A')
      expect(screen.getByTestId('plan-choice-1')).toHaveTextContent('Request revisions')
    })

    it('sends an approving choice as an approval with the plan settings', () => {
      const onRespond = vi.fn<ControlResponseSender>().mockResolvedValue(undefined)
      renderWithChoices(onRespond)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId('plan-choice-0'))
      const { decoded, options } = decodeCall(onRespond)
      expect(decoded.response.request_id).toBe('req-choice')
      expect(decoded.response.response).toMatchObject({ behavior: 'allow', choice: 'Option A' })
      expect(options).toEqual({ planApproval: { permissionMode: '', clearContext: false } })
    })

    // The choice approves the same plan as Approve, so it carries what the reader set
    // on the pills and the switch, exactly as Approve does.
    it('sends the permission mode and the cleared context that the reader set with an approving choice', () => {
      const onRespond = vi.fn<ControlResponseSender>().mockResolvedValue(undefined)
      renderWithChoices(onRespond)
      fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
      fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId('plan-choice-0'))
      const { decoded, options } = decodeCall(onRespond)
      expect(decoded).not.toHaveProperty('permissionMode')
      expect(options).toEqual({ planApproval: { permissionMode: 'bypassPermissions', clearContext: true } })
    })

    // The service refuses plan settings on anything but an approval, so a refusal
    // carries none even when the reader set them.
    it('sends a refusing choice as a refusal with no plan settings', () => {
      const onRespond = vi.fn<ControlResponseSender>().mockResolvedValue(undefined)
      renderWithChoices(onRespond)
      fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
      fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId('plan-choice-1'))
      const { decoded, options } = decodeCall(onRespond)
      expect(onRespond).toHaveBeenCalledOnce()
      expect(decoded.response.request_id).toBe('req-choice')
      expect(decoded.response.response).toMatchObject({ behavior: 'deny', choice: 'Revise' })
      expect(options).toBeUndefined()
    })

    it('draws a refusing choice in the danger colour and an approving one plain', () => {
      renderWithChoices(vi.fn<ControlResponseSender>().mockResolvedValue(undefined))
      fireEvent.click(screen.getByTestId('control-more-actions'))
      expect(screen.getByTestId('plan-choice-0')).not.toHaveClass(dangerMenuItem)
      expect(screen.getByTestId('plan-choice-1')).toHaveClass(dangerMenuItem)
    })

    it('offers no overflow menu for an empty list of choices', () => {
      renderWithChoices(vi.fn<ControlResponseSender>().mockResolvedValue(undefined), false, [])
      expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
      expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    })

    it('hides the choices while the editor holds feedback', () => {
      renderWithChoices(vi.fn<ControlResponseSender>().mockResolvedValue(undefined), true)
      expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
    })
  })

  it('offers no overflow menu for a runtime that offers no choice', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
  })
})
