import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'
import { allowChoicePillGroup, permissionPillGroup } from '~/test-support/controlRequests'
import { CONTROL_ALLOW_CHOICE_ID, createControlAnswerState } from '../../controls/types'
import { CodexControlActions } from './CodexControlRequest'

function makeRequest(params: Record<string, unknown> = {}): ControlRequest {
  return {
    requestId: 'request-1',
    agentId: 'agent-1',
    payload: { method: 'item/commandExecution/requestApproval', params },
  }
}

function makePlanRequest(): ControlRequest {
  return {
    requestId: 'plan-1',
    agentId: 'agent-1',
    payload: { request: { tool_name: 'CodexPlanModePrompt', input: {} } },
  }
}

function renderActions(
  request: ControlRequest,
  hasEditorContent = false,
  answerState = createControlAnswerState(),
) {
  const onRespond = vi.fn().mockResolvedValue(undefined)
  const onSettingChange = vi.fn()
  render(() => (
    <CodexControlActions
      request={request}
      answerState={answerState}
      onRespond={onRespond}
      hasEditorContent={hasEditorContent}
      onTriggerSend={vi.fn()}
      presets={{ bypass: CODEX_BYPASS_SETTINGS, apply: onSettingChange }}
    />
  ))
  return { onRespond, onSettingChange }
}

describe('codex control request actions', () => {
  it('renders Deny and Allow with allow-choice and permission pills', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', 'decline', { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } }] }))

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Reject')
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Allow')
    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Command rule' })).not.toBeChecked()
    // No preset is on, so the group opens on the pill that changes nothing.
    expect(permissionPillGroup().getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    expect(permissionPillGroup().getByRole('radio', { name: 'Bypass' })).not.toBeChecked()
  })

  it('uses the Codex decision that the allow-choice pills select', async () => {
    const { onRespond } = renderActions(makeRequest({ availableDecisions: ['accept', 'decline', { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } }] }))

    fireEvent.click(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Command rule' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toEqual({ acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } })
  })

  it('sends an allowed host-policy amendment from the Host rule pill', async () => {
    const hostDecision = { applyNetworkPolicyAmendment: { network_policy_amendment: { host: 'example.com', action: 'allow' } } }
    const { onRespond } = renderActions(makeRequest({ availableDecisions: ['accept', 'decline', hostDecision] }))

    fireEvent.click(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Host rule' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toEqual(hostDecision)
  })

  it('restores the old Remember selection as its native Codex rule choice', async () => {
    const commandRule = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } }
    const { onRespond } = renderActions(
      makeRequest({ availableDecisions: ['accept', 'acceptForSession', commandRule, 'decline'] }),
      false,
      createControlAnswerState({ switches: { 'control-remember-checkbox': true } }),
    )

    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Command rule' })).toBeChecked()
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toEqual(commandRule)
  })

  it('groups each supported allow decision and appends other decisions', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', 'decline', 'cancel', 'acceptForSession', { applyNetworkPolicyAmendment: { network_policy_amendment: { host: 'example.com', action: 'allow' } } }] }))

    const allowChoices = allowChoicePillGroup('Allow as')
    expect(allowChoices.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(allowChoices.getByRole('radio', { name: 'Session' })).toBeInTheDocument()
    expect(allowChoices.getByRole('radio', { name: 'Host rule' })).toBeInTheDocument()
    expect(screen.getByTestId('control-decision-cancel')).toHaveTextContent('Cancel')
    expect(screen.queryByTestId('control-decision-acceptForSession')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-accept')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-decline')).not.toBeInTheDocument()
  })

  it('keeps allow decisions beyond the four-pill limit as buttons', async () => {
    const overflowHostRule = { applyNetworkPolicyAmendment: { network_policy_amendment: { host: 'overflow.example.com', action: 'allow' } } }
    const { onRespond } = renderActions(makeRequest({
      availableDecisions: [
        'accept',
        'acceptForSession',
        { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } },
        { applyNetworkPolicyAmendment: { network_policy_amendment: { host: 'example.com', action: 'allow' } } },
        overflowHostRule,
        'decline',
      ],
    }))

    expect(allowChoicePillGroup('Allow as').getAllByRole('radio')).toHaveLength(4)
    const overflow = screen.getByTestId('control-decision-applyNetworkPolicyAmendment')
    expect(overflow).toHaveTextContent('Allow Host & Remember')
    await fireEvent.click(overflow)

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toEqual(overflowHostRule)
  })

  it('clamps an obsolete saved allow choice to Once', async () => {
    const { onRespond } = renderActions(
      makeRequest({ availableDecisions: ['accept', 'decline', { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } }] }),
      false,
      createControlAnswerState({ choices: { [CONTROL_ALLOW_CHOICE_ID]: 'gone' } }),
    )

    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Once' })).toBeChecked()
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toBe('accept')
  })

  it('uses only the negative decision that Codex offers', async () => {
    const { onRespond } = renderActions(makeRequest({ availableDecisions: ['accept', 'cancel'] }))

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    expect(screen.queryByRole('radiogroup', { name: 'Allow as' })).not.toBeInTheDocument()
    await fireEvent.click(screen.getByTestId('control-deny-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toBe('cancel')
  })

  it('sends the native permission grant response', async () => {
    const request = makeRequest()
    request.payload = {
      method: 'item/permissions/requestApproval',
      params: {
        permissions: {
          network: { enabled: true },
          fileSystem: null,
        },
      },
    }
    const { onRespond } = renderActions(request)

    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Once' })).toBeChecked()
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      jsonrpc: '2.0',
      id: 'request-1',
      result: {
        permissions: { network: { enabled: true } },
        scope: 'turn',
      },
    })
  })

  it('can grant requested permissions for the session', async () => {
    const request = makeRequest()
    request.payload = {
      method: 'item/permissions/requestApproval',
      params: { permissions: { network: { enabled: true } } },
    }
    const { onRespond } = renderActions(request)

    fireEvent.click(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Session' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.scope).toBe('session')
  })

  it('restores the old Remember selection as Session for an in-flight permission request', () => {
    const request = makeRequest()
    request.payload = {
      method: 'item/permissions/requestApproval',
      params: { permissions: { network: { enabled: true } } },
    }

    renderActions(request, false, createControlAnswerState({
      switches: { 'control-remember-checkbox': true },
    }))

    expect(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Session' })).toBeChecked()
  })

  it('lets a new Once choice override a restored Remember selection', async () => {
    const request = makeRequest()
    request.payload = {
      method: 'item/permissions/requestApproval',
      params: { permissions: { network: { enabled: true } } },
    }
    const answerState = createControlAnswerState({
      switches: { 'control-remember-checkbox': true },
    })
    const { onRespond } = renderActions(request, false, answerState)

    fireEvent.click(allowChoicePillGroup('Allow as').getByRole('radio', { name: 'Once' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.scope).toBe('turn')
    expect(answerState.choices()).toEqual({ [CONTROL_ALLOW_CHOICE_ID]: 'turn' })
  })

  it('denies a permission request with an empty grant', async () => {
    const request = makeRequest()
    request.payload = {
      method: 'item/permissions/requestApproval',
      params: { permissions: { network: { enabled: true } } },
    }
    const { onRespond } = renderActions(request)

    await fireEvent.click(screen.getByTestId('control-deny-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result).toEqual({ permissions: {}, scope: 'turn' })
  })

  it('groups session and command-rule decisions together', () => {
    renderActions(makeRequest({
      availableDecisions: [
        'accept',
        'decline',
        'acceptForSession',
        { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } },
      ],
    }))

    const allowChoices = allowChoicePillGroup('Allow as')
    expect(allowChoices.getByRole('radio', { name: 'Once' })).toBeChecked()
    expect(allowChoices.getByRole('radio', { name: 'Session' })).toBeInTheDocument()
    expect(allowChoices.getByRole('radio', { name: 'Command rule' })).toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-acceptForSession')).not.toBeInTheDocument()
  })

  it('ignores malformed available decisions', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', null, 3, {}, 'decline'] }))

    expect(screen.queryByTestId('control-decision-unknown')).not.toBeInTheDocument()
  })

  it('sends the request before it applies all Codex bypass settings', async () => {
    const { onRespond, onSettingChange } = renderActions(makeRequest({ availableDecisions: ['accept', 'decline'] }))

    let finishResponse!: () => void
    onRespond.mockReturnValue(new Promise<void>((resolve) => {
      finishResponse = resolve
    }))

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    expect(onSettingChange).not.toHaveBeenCalled()
    finishResponse()
    await vi.waitFor(() => expect(onSettingChange).toHaveBeenCalledOnce())
    expect(onSettingChange).toHaveBeenCalledWith(CODEX_BYPASS_SETTINGS)
  })

  it('shows only Send feedback when the editor has content', () => {
    renderActions(makeRequest({
      availableDecisions: ['accept', 'decline', 'cancel', { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['rm'] } }],
    }), true)

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
    expect(screen.queryByRole('radiogroup', { name: 'Allow as' })).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-cancel')).not.toBeInTheDocument()
  })

  it('sends a context-clearing plan approval from the shared plan footer', async () => {
    const { onRespond } = renderActions(makePlanRequest())

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Reject')
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Approve')
    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      codexPlanModePrompt: true,
      clearContext: true,
      response: { request_id: 'plan-1', response: { behavior: 'allow' } },
    })
  })

  it('embeds the bypass mode in a plan approval when Bypass is selected', async () => {
    const { onRespond, onSettingChange } = renderActions(makePlanRequest())

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      codexPlanModePrompt: true,
      permissionMode: 'never',
      response: { request_id: 'plan-1', response: { behavior: 'allow' } },
    })
    // The mode travels INSIDE the response; a second settings change would race
    // the restart a context-clearing approval triggers, so the handler never fires.
    expect(onSettingChange).not.toHaveBeenCalled()
  })
})
