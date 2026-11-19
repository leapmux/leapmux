import type { Mock } from 'vitest'
import type { PlanChoice } from '../model/controlPrompt'
import type { ControlDecisionSender } from './types'
import type { PlanDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'
import { dangerMenuItem } from '~/styles/shared.css'
import { permissionPillGroup, testControlRequest } from '~/test-support/controlRequests'
import { createControlAnswerState } from './types'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return testControlRequest({
    requestId,
    agentId,
    prompt: { kind: 'plan', text: '1. Do it' },
  })
}

/** The plan decision of one call of the sender, or a failure for a call that sent none. */
function planDecisionOf(onDecide: Mock<ControlDecisionSender>, call = 0): PlanDecision {
  const decision = onDecide.mock.calls[call]?.[0]
  if (decision?.kind.case !== 'plan')
    throw new Error(`call ${call} sent no plan decision`)
  return decision.kind.value
}

describe('ExitPlanModeActions', () => {
  it('shows Reject, Approve, the Clear Context switch, and the permission pills when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: {
            sets: { permissionMode: 'auto' },
          },
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
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
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-reject-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-clear-context-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
  })

  it('sends clearContext when Clear Context is checked', () => {
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-clear', 'agent-clear')}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(planDecisionOf(onDecide)).toMatchObject({ approve: true, clearContext: true })
  })

  it('approves with the bypass mode when Bypass is selected', () => {
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    const apply = vi.fn()
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply,
        }}
      />
    ))

    // Select bypass permissions, then approve.
    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onDecide).toHaveBeenCalledOnce()
    expect(planDecisionOf(onDecide)).toMatchObject({ approve: true, permissionMode: 'bypassPermissions', clearContext: false })
    // One decision carries the approval and the plan settings. A separate settings call could race the restart.
    expect(apply).not.toHaveBeenCalled()
  })

  it('approves with the smart mode when Smart is selected', () => {
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    const apply = vi.fn()

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: {
            sets: { permissionMode: 'auto' },
          },
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply,
        }}
      />
    ))

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Smart' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(planDecisionOf(onDecide)).toMatchObject({ approve: true, permissionMode: 'auto' })
    expect(apply).not.toHaveBeenCalled()
  })

  it('carries the smart mode when the user touches no pill', () => {
    // Smart is the opening choice, so an approval attaches its mode with no
    // click on the group.
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: {
            sets: { permissionMode: 'auto' },
          },
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(planDecisionOf(onDecide)).toMatchObject({ approve: true, permissionMode: 'auto' })
  })

  it('carries no mode when the catalog offers no smart preset', () => {
    // The opening choice clamps to Unchanged, so an untouched group leaves the
    // agent's permission mode where it is.
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(planDecisionOf(onDecide)).toMatchObject({ approve: true, permissionMode: '', clearContext: false })
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
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { allow_all: 'on' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).toBeNull()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  it('approves with no permission mode for a plain approve', () => {
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    const request = makeRequest('req-42', 'agent-5')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          bypass: {
            sets: { permissionMode: 'bypassPermissions' },
          },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onDecide).toHaveBeenCalledOnce()
    expect(planDecisionOf(onDecide)).toMatchObject({
      approve: true,
      choiceId: '',
      permissionMode: '',
      clearContext: false,
      feedback: '',
    })
  })

  it('does not show the permission pills when presets are absent', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  describe('with the choices a runtime offers', () => {
    const choices = [
      { id: 'Option A', label: 'Approve: Option A', effect: 'approve' },
      { id: 'Revise', label: 'Request revisions', effect: 'refuse' },
      { id: 'Later', label: 'Save for later', effect: 'other' },
    ] satisfies PlanChoice[]

    function renderWithChoices(onDecide: Mock<ControlDecisionSender>, hasEditorContent = false, offered: PlanChoice[] = choices) {
      render(() => (
        <ExitPlanModeActions
          request={makeRequest('req-choice', 'agent-choice')}
          answerState={createControlAnswerState()}
          onDecide={onDecide}
          hasEditorContent={hasEditorContent}
          onTriggerSend={() => {}}
          presets={{
            bypass: {
              sets: { permissionMode: 'bypassPermissions' },
            },
            apply: vi.fn(),
          }}
          choices={offered}
        />
      ))
    }

    // A runtime can state what each approach does. The reader needs it to choose,
    // because the plan text need not repeat it.
    it('states what each approach does in the tooltip of its menu item', () => {
      vi.useFakeTimers()
      try {
        renderWithChoices(vi.fn<ControlDecisionSender>().mockResolvedValue(undefined), false, [
          {
            id: 'Option A',
            label: 'Approve: Option A',
            description: 'Split the parser first',
            effect: 'approve',
          },
          { id: 'Revise', label: 'Request revisions', effect: 'refuse' },
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
      renderWithChoices(vi.fn<ControlDecisionSender>().mockResolvedValue(undefined))
      fireEvent.click(screen.getByTestId('control-more-actions'))
      expect(screen.getByTestId('plan-choice-0')).toHaveTextContent('Approve: Option A')
      expect(screen.getByTestId('plan-choice-1')).toHaveTextContent('Request revisions')
      expect(screen.getByTestId('plan-choice-2')).toHaveTextContent('Save for later')
    })

    it('sends an approving choice as an approval with the plan settings', () => {
      const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
      renderWithChoices(onDecide)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId('plan-choice-0'))
      expect(planDecisionOf(onDecide)).toMatchObject({
        approve: true,
        choiceId: 'Option A',
        permissionMode: '',
        clearContext: false,
      })
    })

    // The choice approves the same plan as Approve, so it carries what the reader set
    // on the pills and the switch, exactly as Approve does.
    it('sends the permission mode and the cleared context that the reader set with an approving choice', () => {
      const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
      renderWithChoices(onDecide)
      fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
      fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId('plan-choice-0'))
      expect(planDecisionOf(onDecide)).toMatchObject({
        approve: true,
        choiceId: 'Option A',
        permissionMode: 'bypassPermissions',
        clearContext: true,
      })
    })

    // The Worker refuses plan settings on anything but an approval, so a choice that
    // does not approve carries none even when the reader set them.
    it.each([
      ['refusing', 'plan-choice-1', 'Revise'],
      ['neutral', 'plan-choice-2', 'Later'],
    ])('sends a %s choice with no plan settings', (_effect, testId, choiceId) => {
      const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
      renderWithChoices(onDecide)
      fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
      fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
      fireEvent.click(screen.getByTestId('control-more-actions'))
      fireEvent.click(screen.getByTestId(testId))
      expect(onDecide).toHaveBeenCalledOnce()
      expect(planDecisionOf(onDecide)).toMatchObject({
        approve: false,
        choiceId,
        permissionMode: '',
        clearContext: false,
        feedback: '',
      })
    })

    it('draws a refusing choice in the danger colour and the others plain', () => {
      renderWithChoices(vi.fn<ControlDecisionSender>().mockResolvedValue(undefined))
      fireEvent.click(screen.getByTestId('control-more-actions'))
      expect(screen.getByTestId('plan-choice-0')).not.toHaveClass(dangerMenuItem)
      expect(screen.getByTestId('plan-choice-1')).toHaveClass(dangerMenuItem)
      expect(screen.getByTestId('plan-choice-2')).not.toHaveClass(dangerMenuItem)
    })

    it('offers no overflow menu for an empty list of choices', () => {
      renderWithChoices(vi.fn<ControlDecisionSender>().mockResolvedValue(undefined), false, [])
      expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
      expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    })

    it('hides the choices while the editor holds feedback', () => {
      renderWithChoices(vi.fn<ControlDecisionSender>().mockResolvedValue(undefined), true)
      expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
    })
  })

  // The composer sends the refusal, because the editor text is what to change.
  it.each([false, true])('hands Reject to the composer and decides nothing itself (editor content=%s)', (hasEditorContent) => {
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    const onTriggerSend = vi.fn()
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={hasEditorContent}
        onTriggerSend={onTriggerSend}
      />
    ))
    fireEvent.click(screen.getByTestId('plan-reject-btn'))
    expect(onTriggerSend).toHaveBeenCalledOnce()
    expect(onDecide).not.toHaveBeenCalled()
  })

  it('offers no overflow menu for a runtime that offers no choice', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.queryByTestId('control-more-actions')).not.toBeInTheDocument()
  })
})
