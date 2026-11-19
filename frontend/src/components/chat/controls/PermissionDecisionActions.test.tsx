import type { PermissionOption, PermissionPrompt } from '../model/controlPrompt'
import type { PermissionPresetController } from '../providerSettings'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { allowChoicePillGroup, permissionPillGroup, testControlRequest } from '~/test-support/controlRequests'
import { createControlAnswerState } from './types'

/**
 * The shared decision row, read through each runtime's own option list.
 *
 * Every agent that states its own answers sends a different vocabulary of option
 * IDs, and this row lays them out by their KINDS instead. The cases below are the
 * shapes the Worker's readers send, each rendered through the real banner, so a
 * layout that stopped reading an option list would fail here rather than draw an
 * Allow that answers nothing.
 */
function decidedOptionId(onDecide: ReturnType<typeof vi.fn>, call: number): string | undefined {
  const decision = onDecide.mock.calls[call]?.[0] as ControlDecision | undefined
  return decision?.kind.case === 'permission'
    ? decision.kind.value.optionId
    : undefined
}

function renderPermission(
  provider: AgentProvider,
  requestId: string,
  permission: PermissionPrompt,
  presets?: PermissionPresetController,
  answerState = createControlAnswerState(),
) {
  const onDecide = vi.fn().mockResolvedValue(undefined)
  render(() => (
    <ControlRequestActions
      agentProvider={provider}
      request={testControlRequest({
        agentId: 'agent1',
        requestId,
        prompt: { kind: 'permission', permission },
      })}
      answerState={answerState}
      onDecide={onDecide}
      hasEditorContent={false}
      onTriggerSend={vi.fn()}
      {...(presets === undefined
        ? {}
        : { presets })}
    />
  ))
  return { onDecide }
}

function renderDecisions(
  provider: AgentProvider,
  requestId: string,
  options: PermissionOption[],
  presets?: PermissionPresetController,
) {
  return renderPermission(provider, requestId, { options }, presets)
}

describe('PermissionDecisionActions', () => {
  it.each([
    ['Switch this turn', 'once'],
    ['Always switch', 'session'],
    ['Decline', 'reject'],
  ])('sends the independent %s decision without permission presets', async (label, optionId) => {
    const apply = vi.fn()
    const answerState = createControlAnswerState()
    answerState.setChoices({ 'control-permissions-pill': 'bypass' })
    // Copilot's automatic model switch, as the Worker reads it.
    const { onDecide } = renderPermission(AgentProvider.GITHUB_COPILOT, 'switch', {
      title: 'Copilot asks to switch to Auto',
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Switch this turn' },
        { optionId: 'session', kind: 'allow_always', name: 'Always switch' },
        { optionId: 'reject', kind: 'reject_once', name: 'Decline' },
      ],
      independentOptions: true,
    }, {
      bypass: {
        sets: { permissionMode: 'allow_all' },
      },
      apply,
    }, answerState)
    expect(screen.getAllByRole('button').map(button => button.textContent)).toEqual(['Switch this turn', 'Always switch', 'Decline'])
    expect(screen.queryByRole('radiogroup')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: label }))
    expect(onDecide).toHaveBeenCalledTimes(1)
    expect(decidedOptionId(onDecide, 0)).toBe(optionId)
    expect(apply).not.toHaveBeenCalled()
  })

  /** Goose's wire shape: four kind-named options, emitted allow-first. */
  function renderGooseActions() {
    const apply = vi.fn()
    const { onDecide } = renderDecisions(AgentProvider.GOOSE, '7', [
      { optionId: 'allow_always', kind: 'allow_always', name: 'allow_always' },
      { optionId: 'allow_once', kind: 'allow_once', name: 'allow_once' },
      { optionId: 'reject_once', kind: 'reject_once', name: 'reject_once' },
      { optionId: 'reject_always', kind: 'reject_always', name: 'reject_always' },
    ], {
      smart: {
        sets: { permissionMode: 'smart_approve' },
      },
      bypass: {
        sets: { permissionMode: 'auto' },
      },
      apply,
    })
    return { onDecide, apply }
  }

  /** Kilo's wire shape: an allow_always with no reject_always. */
  function renderKiloShapeActions() {
    return renderDecisions(AgentProvider.KILO, '8', [
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
    ])
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

    const scope = allowChoicePillGroup()
    expect(scope.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(scope.getByRole('radio', { name: 'Always' })).not.toBeChecked()
    const pill = permissionPillGroup()
    // An ordinary request opens on the preset the session has on. This one
    // reports none, so the group opens on the pill that changes nothing.
    expect(pill.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    expect(pill.getByRole('radio', { name: 'Smart' })).not.toBeChecked()
    // The always options live in the scope group, not as their own buttons.
    expect(screen.queryByTestId('control-decision-allow_always')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-reject_always')).not.toBeInTheDocument()
  })

  it('sizes the decisions with the shared compact style', () => {
    // This row builds its own decisions rather than going through
    // `ControlDecisionFooter`, so it carries the class on its own. The footer
    // slot states no size, so each action uses the shared compact style.
    renderGooseActions()

    expect(screen.getByTestId('control-deny-btn')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('control-allow-btn')).toHaveClass(compactControl)
    expect(screen.getByTestId('control-allow-btn')).not.toHaveClass('outline')
  })

  it('sends the once options while Once is selected', async () => {
    const { onDecide } = renderGooseActions()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 0)).toBe('allow_once')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decidedOptionId(onDecide, 1)).toBe('reject_once')
  })

  it('sends the always options once a scope beyond Once is selected', async () => {
    const { onDecide } = renderGooseActions()

    fireEvent.click(allowChoicePillGroup().getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 0)).toBe('allow_always')

    // A remembering scope also upgrades Deny for the one agent offering reject_always.
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decidedOptionId(onDecide, 1)).toBe('reject_always')
  })

  it('keeps the reject-once option under a remembering scope when the agent offers no reject_always', async () => {
    const { onDecide } = renderKiloShapeActions()

    fireEvent.click(allowChoicePillGroup().getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decidedOptionId(onDecide, 0)).toBe('reject')

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 1)).toBe('always')
  })

  it('draws no scope group when the agent offers no always option', () => {
    renderDecisions(AgentProvider.GOOSE, '9', [
      { optionId: 'once', kind: 'allow_once', name: 'Allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
    ])

    expect(screen.queryByRole('radiogroup', { name: 'Allow scope' })).not.toBeInTheDocument()
  })

  it('draws no permission pills when no allow option can apply them', () => {
    // A reject-only payload has no positive action, so no selection the group
    // offers could ever act; drawing it would be a control that silently does
    // nothing.
    renderDecisions(
      AgentProvider.GOOSE,
      '10',
      [
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
      {
        smart: {
          sets: { permissionMode: 'smart_approve' },
        },
        bypass: {
          sets: { permissionMode: 'auto' },
        },
        apply: vi.fn(),
      },
    )

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
  })

  it('states the duration on the Allow button when the agent offers no once option', async () => {
    const { onDecide } = renderDecisions(AgentProvider.GOOSE, '11', [
      { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
    ])

    expect(screen.getByTestId('control-allow-btn').textContent).toBe('Always allow')
    expect(screen.queryByRole('radiogroup', { name: 'Allow scope' })).not.toBeInTheDocument()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 0)).toBe('always')
  })

  it('applies the chosen permission preset after an allow and never after a reject', async () => {
    const { onDecide, apply } = renderGooseActions()

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(onDecide).toHaveBeenCalledTimes(1)
    expect(apply).toHaveBeenCalledWith({
      sets: { permissionMode: 'auto' },
    })

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(onDecide).toHaveBeenCalledTimes(2)
    expect(apply).toHaveBeenCalledTimes(1)
  })

  it('applies no preset when an extra option outside the allow family is clicked', async () => {
    // The pill's contract is "applies when the request's positive action is
    // taken": an extra button carrying an invented kind (a future agent's
    // answer variant) is not that action, so it must not switch the mode.
    const apply = vi.fn()
    const { onDecide } = renderDecisions(AgentProvider.GOOSE, '15', [
      { optionId: 'once', kind: 'allow_once', name: 'Allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      { optionId: 'ask', kind: 'ask_user', name: 'Ask the user' },
    ], {
      bypass: {
        sets: { permissionMode: 'yolo' },
      },
      apply,
    })

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    await fireEvent.click(screen.getByTestId('control-decision-ask'))

    expect(onDecide).toHaveBeenCalledOnce()
    expect(decidedOptionId(onDecide, 0)).toBe('ask')
    expect(apply).not.toHaveBeenCalled()
  })

  it('draws a Once / Session / Project pill group for Reasonix\'s two always scopes', async () => {
    const { onDecide } = renderDecisions(AgentProvider.REASONIX, '13', [
      { optionId: 'reasonix_write_once', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'reasonix_write_session', kind: 'allow_always', name: 'Allow these directories for this session' },
      { optionId: 'reasonix_write_project', kind: 'allow_always', name: 'Add to project allow_write' },
      { optionId: 'reasonix_write_deny', kind: 'reject_once', name: 'Reject' },
    ])

    // The scope group replaces the extra button the project option used to be.
    const scope = allowChoicePillGroup()
    expect(scope.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(scope.getByRole('radio', { name: 'Session' })).toBeInTheDocument()
    expect(scope.getByRole('radio', { name: 'Project' })).toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-reasonix_write_project')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn').textContent).toBe('Allow')

    // Allow sends the selected scope's option, Once first, Project after a pick.
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 0)).toBe('reasonix_write_once')

    fireEvent.click(scope.getByRole('radio', { name: 'Project' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decidedOptionId(onDecide, 1)).toBe('reasonix_write_project')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decidedOptionId(onDecide, 2)).toBe('reasonix_write_deny')
  })
})
