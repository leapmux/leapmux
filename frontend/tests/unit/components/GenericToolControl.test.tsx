import type { AskQuestionState } from '~/components/chat/controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { GenericToolActions } from '~/components/chat/controls/GenericToolControl'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'Bash', input: { command: 'ls' } },
    },
  }
}

function makeAskState(): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>({})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>({})
  const [currentPage, setCurrentPage] = createSignal(0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

describe('genericToolActions', () => {
  it('shows Reject, Allow, and Bypass Permissions when no editor content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        onPermissionModeChange={vi.fn()}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeTruthy()
    expect(screen.getByTestId('control-deny-btn').textContent).toBe('Reject')
    expect(screen.getByTestId('control-allow-btn')).toBeTruthy()
    expect(screen.getByTestId('control-bypass-btn')).toBeTruthy()
  })

  it('shows only Send Feedback when editor has content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        onPermissionModeChange={vi.fn()}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeTruthy()
    expect(screen.getByTestId('control-deny-btn').textContent).toBe('Send Feedback')
    expect(screen.queryByTestId('control-allow-btn')).toBeNull()
    expect(screen.queryByTestId('control-bypass-btn')).toBeNull()
  })

  it('sends allow response and changes permission mode when bypass is clicked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const onPermissionModeChange = vi.fn()
    const request = makeRequest('req-42', 'agent-7')

    render(() => (
      <GenericToolActions
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
    expect(agentId).toBe('agent-7')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')

    // Verify permission mode change
    expect(onPermissionModeChange).toHaveBeenCalledOnce()
    expect(onPermissionModeChange).toHaveBeenCalledWith('bypassPermissions')
  })

  it('has a tooltip on the bypass button', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        onPermissionModeChange={vi.fn()}
      />
    ))

    expect(screen.getByTestId('control-bypass-btn').getAttribute('title'))
      .toBe('Allow this request and stop asking for permissions')
  })
})
