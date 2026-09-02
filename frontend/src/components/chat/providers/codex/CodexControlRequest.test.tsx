import type { AskQuestionState } from '../../controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { CodexControlActions } from './CodexControlRequest'
import { CODEX_BYPASS_PERMISSION_SETTINGS } from './constants'

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

function makeAskState(): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>({})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>({})
  const [currentPage, setCurrentPage] = createSignal(0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

function renderActions(request: ControlRequest, hasEditorContent = false) {
  const onRespond = vi.fn().mockResolvedValue(undefined)
  const onPermissionModeChange = vi.fn()
  const onSettingChange = vi.fn()
  render(() => (
    <CodexControlActions
      request={request}
      askState={makeAskState()}
      onRespond={onRespond}
      hasEditorContent={hasEditorContent}
      onTriggerSend={vi.fn()}
      bypassPermissionMode="never"
      onPermissionModeChange={onPermissionModeChange}
      onSettingChange={onSettingChange}
    />
  ))
  return { onRespond, onPermissionModeChange, onSettingChange }
}

describe('codex control request actions', () => {
  it('renders Deny and Allow with Remember and Bypass Permissions switches', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', 'decline', { acceptWithExecpolicyAmendment: { match: 'rm' } }] }))

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Deny')
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Allow')
    expect(screen.getByTestId('control-remember-checkbox')).toHaveTextContent('Remember')
    expect(screen.getByTestId('control-bypass-permissions-checkbox')).toHaveTextContent('Bypass Permissions')
  })

  it('uses the remembered Codex decision only when Remember is checked', async () => {
    const { onRespond } = renderActions(makeRequest({ availableDecisions: ['accept', 'decline', { acceptWithExecpolicyAmendment: { match: 'rm' } }] }))

    fireEvent.click(screen.getByTestId('control-remember-checkbox').querySelector('input')!)
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).result.decision).toEqual({ acceptWithExecpolicyAmendment: { match: 'rm' } })
  })

  it('appends decisions that the fixed controls do not cover', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', 'decline', 'cancel', 'acceptForSession', { applyNetworkPolicyAmendment: true }] }))

    expect(screen.getByTestId('control-decision-applyNetworkPolicyAmendment')).toHaveTextContent('Apply Network Policy')
    expect(screen.getByTestId('control-decision-cancel')).toHaveTextContent('Cancel')
    expect(screen.queryByTestId('control-decision-accept')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-decline')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-acceptForSession')).not.toBeInTheDocument()
  })

  it('appends a second persistent allow decision that Remember does not select', () => {
    renderActions(makeRequest({
      availableDecisions: [
        'accept',
        'decline',
        'acceptForSession',
        { acceptWithExecpolicyAmendment: { match: 'rm' } },
      ],
    }))

    expect(screen.getByTestId('control-remember-checkbox')).toBeInTheDocument()
    expect(screen.getByTestId('control-decision-acceptForSession')).toHaveTextContent('Allow for Session')
  })

  it('ignores malformed available decisions', () => {
    renderActions(makeRequest({ availableDecisions: ['accept', null, 3, {}, 'decline'] }))

    expect(screen.queryByTestId('control-decision-unknown')).not.toBeInTheDocument()
  })

  it('sends the request before it applies all Codex bypass settings', async () => {
    const { onRespond, onPermissionModeChange, onSettingChange } = renderActions(makeRequest({ availableDecisions: ['accept', 'decline'] }))

    let finishResponse!: () => void
    onRespond.mockReturnValue(new Promise<void>((resolve) => {
      finishResponse = resolve
    }))

    fireEvent.click(screen.getByTestId('control-bypass-permissions-checkbox').querySelector('input')!)
    fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    expect(onSettingChange).not.toHaveBeenCalled()
    finishResponse()
    await vi.waitFor(() => expect(onSettingChange).toHaveBeenCalledOnce())
    expect(onSettingChange).toHaveBeenCalledWith({ sets: CODEX_BYPASS_PERMISSION_SETTINGS })
    expect(onPermissionModeChange).not.toHaveBeenCalled()
  })

  it('shows only Send feedback when the editor has content', () => {
    renderActions(makeRequest({
      availableDecisions: ['accept', 'decline', 'cancel', { acceptWithExecpolicyAmendment: { match: 'rm' } }],
    }), true)

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-remember-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-bypass-permissions-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-decision-cancel')).not.toBeInTheDocument()
  })

  it('sends a context-clearing plan approval from the shared plan footer', async () => {
    const { onRespond } = renderActions(makePlanRequest())

    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Reject')
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Approve')
    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      codexPlanModePrompt: true,
      clearContext: true,
      response: { request_id: 'plan-1', response: { behavior: 'allow' } },
    })
  })
})
