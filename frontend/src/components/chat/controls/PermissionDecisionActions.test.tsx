import type { PermissionPresetController } from '../providerSettings'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { allowChoicePillGroup, permissionPillGroup } from '~/test-support/controlRequests'
import { providerFor } from '../providers/registry'
import { createControlAnswerState } from './types'
import '../providers'

/**
 * The shared decision row, read through each runtime's own option list.
 *
 * Every agent that states its own answers sends a different vocabulary of option
 * IDS, and this row lays them out by their KINDS instead. The cases below are the
 * four shapes those agents send, each rendered through the real banner: the
 * provider's `extractControl` fills the model and the banner picks this row, so a
 * plugin that stopped filling the options would fail here rather than draw an
 * Allow that answers nothing.
 */
function decodeOptionId(content: Uint8Array): string {
  return JSON.parse(new TextDecoder().decode(content)).result.outcome.optionId
}

function renderDecisions(
  provider: AgentProvider,
  requestId: string,
  options: Array<Record<string, string>>,
  presets?: PermissionPresetController,
) {
  const onRespond = vi.fn().mockResolvedValue(undefined)
  render(() => (
    <ControlRequestActions
      agentProvider={provider}
      request={{ agentId: 'agent1', requestId, payload: { params: { options } } }}
      answerState={createControlAnswerState()}
      onRespond={onRespond}
      hasEditorContent={false}
      onTriggerSend={vi.fn()}
      {...(presets === undefined ? {} : { presets })}
    />
  ))
  return { onRespond }
}

describe('the selected-option reply of the Agent Client Protocol family', () => {
  it('sends the chosen id as a selected outcome, under the worker request id', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    await providerFor(AgentProvider.GOOSE)!.controls?.sendPermissionOption!(onRespond, '7', 'proceed_once')

    expect(onRespond).toHaveBeenCalledTimes(1)
    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [content] = respondCall ?? []
    expect(JSON.parse(new TextDecoder().decode(content))).toEqual({
      jsonrpc: '2.0',
      id: '7',
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'proceed_once',
        },
      },
    })
  })
})

describe('PermissionDecisionActions', () => {
  /** Goose's wire shape: four kind-named options, emitted allow-first. */
  function renderGooseActions() {
    const apply = vi.fn()
    const { onRespond } = renderDecisions(AgentProvider.GOOSE, '7', [
      { optionId: 'allow_always', kind: 'allow_always', name: 'allow_always' },
      { optionId: 'allow_once', kind: 'allow_once', name: 'allow_once' },
      { optionId: 'reject_once', kind: 'reject_once', name: 'reject_once' },
      { optionId: 'reject_always', kind: 'reject_always', name: 'reject_always' },
    ], {
      smart: { sets: { permissionMode: 'smart_approve' } },
      bypass: { sets: { permissionMode: 'auto' } },
      apply,
    })
    return { onRespond, apply }
  }

  /** OpenCode's wire shape: an allow_always with no reject_always. */
  function renderOpenCodeShapeActions() {
    return renderDecisions(AgentProvider.OPENCODE, '8', [
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
    const { onRespond } = renderGooseActions()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('allow_once')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1]?.[0])).toBe('reject_once')
  })

  it('sends the always options once a scope beyond Once is selected', async () => {
    const { onRespond } = renderGooseActions()

    fireEvent.click(allowChoicePillGroup().getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('allow_always')

    // A remembering scope also upgrades Deny for the one agent offering reject_always.
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1]?.[0])).toBe('reject_always')
  })

  it('keeps the reject-once option under a remembering scope when the agent offers no reject_always', async () => {
    const { onRespond } = renderOpenCodeShapeActions()

    fireEvent.click(allowChoicePillGroup().getByRole('radio', { name: 'Always' }))
    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('reject')

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1]?.[0])).toBe('always')
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
      [{ optionId: 'reject', kind: 'reject_once', name: 'Reject' }],
      { smart: { sets: { permissionMode: 'smart_approve' } }, bypass: { sets: { permissionMode: 'auto' } }, apply: vi.fn() },
    )

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
  })

  it('states the duration on the Allow button when the agent offers no once option', async () => {
    const { onRespond } = renderDecisions(AgentProvider.GOOSE, '11', [
      { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
    ])

    expect(screen.getByTestId('control-allow-btn').textContent).toBe('Always allow')
    expect(screen.queryByRole('radiogroup', { name: 'Allow scope' })).not.toBeInTheDocument()

    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('always')
  })

  it('applies the chosen permission preset after an allow and never after a reject', async () => {
    const { onRespond, apply } = renderGooseActions()

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(onRespond).toHaveBeenCalledTimes(1)
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(onRespond).toHaveBeenCalledTimes(2)
    expect(apply).toHaveBeenCalledTimes(1)
  })

  it('applies no preset when an extra option outside the allow family is clicked', async () => {
    // The pill's contract is "applies when the request's positive action is
    // taken": an extra button carrying an invented kind (a future agent's
    // answer variant) is not that action, so it must not switch the mode.
    const apply = vi.fn()
    const { onRespond } = renderDecisions(AgentProvider.GOOSE, '15', [
      { optionId: 'once', kind: 'allow_once', name: 'Allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      { optionId: 'ask', kind: 'ask_user', name: 'Ask the user' },
    ], { bypass: { sets: { permissionMode: 'yolo' } }, apply })

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    await fireEvent.click(screen.getByTestId('control-decision-ask'))

    expect(onRespond).toHaveBeenCalledOnce()
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('ask')
    expect(apply).not.toHaveBeenCalled()
  })

  it('draws a Once / Session / Project pill group for Reasonix\'s two always scopes', async () => {
    const { onRespond } = renderDecisions(AgentProvider.REASONIX, '13', [
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
    expect(decodeOptionId(onRespond.mock.calls[0]?.[0])).toBe('reasonix_write_once')

    fireEvent.click(scope.getByRole('radio', { name: 'Project' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))
    expect(decodeOptionId(onRespond.mock.calls[1]?.[0])).toBe('reasonix_write_project')

    await fireEvent.click(screen.getByTestId('control-deny-btn'))
    expect(decodeOptionId(onRespond.mock.calls[2]?.[0])).toBe('reasonix_write_deny')
  })
})
