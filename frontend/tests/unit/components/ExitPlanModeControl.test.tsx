import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'
import type { AskQuestionState } from '~/components/chat/controls/types'
import type { ControlRequest } from '~/stores/control.store'

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
        onPermissionModeChange={vi.fn()}
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
        onPermissionModeChange={vi.fn()}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeTruthy()
    expect(screen.queryByTestId('plan-approve-btn')).toBeNull()
    expect(screen.queryByTestId('control-bypass-btn')).toBeNull()
  })

  it('sends allow response and changes permission mode when bypass is clicked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const onPermissionModeChange = vi.fn()
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        onPermissionModeChange={onPermissionModeChange}
      />
    ))

    fireEvent.click(screen.getByTestId('control-bypass-btn'))

    // Verify allow response was sent
    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-3')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-99')
    expect(decoded.response.response.behavior).toBe('allow')

    // Verify permission mode change
    expect(onPermissionModeChange).toHaveBeenCalledOnce()
    expect(onPermissionModeChange).toHaveBeenCalledWith('bypassPermissions')
  })

  it('has a tooltip on the bypass button', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        onPermissionModeChange={vi.fn()}
      />
    ))

    expect(screen.getByTestId('control-bypass-btn').getAttribute('title'))
      .toBe('Approve this plan and stop asking for permissions')
  })
})
