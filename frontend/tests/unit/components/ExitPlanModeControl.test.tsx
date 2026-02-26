import type { AskQuestionState } from '~/components/chat/controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

function makeAskState(): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>({})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>({})
  const [currentPage, setCurrentPage] = createSignal(0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

describe('exitPlanModeActions', () => {
  it('shows Reject, Approve and Bypass Permissions when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeTruthy()
    expect(screen.getByTestId('plan-approve-btn')).toBeTruthy()
    expect(screen.getByTestId('control-bypass-btn')).toBeTruthy()
  })

  it('shows only Reject when editor has content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeTruthy()
    expect(screen.queryByTestId('plan-approve-btn')).toBeNull()
    expect(screen.queryByTestId('control-bypass-btn')).toBeNull()
  })

  it('sends allow response with permissionMode in the control response', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    fireEvent.click(screen.getByTestId('control-bypass-btn'))

    // Verify allow response was sent with permissionMode included
    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-3')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-99')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.permissionMode).toBe('bypassPermissions')
  })

  it('sends allow response without permissionMode for normal approve', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-42', 'agent-5')

    render(() => (
      <ExitPlanModeActions
        request={request}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-5')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.permissionMode).toBeUndefined()
  })

  it('has a tooltip on the bypass button', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('control-bypass-btn').getAttribute('title'))
      .toBe('Approve this plan and stop asking for permissions')
  })
})
