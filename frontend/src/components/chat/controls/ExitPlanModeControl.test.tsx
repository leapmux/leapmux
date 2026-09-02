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
  it('shows Reject, Approve, and the plan switches when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypassPermissionMode="bypassPermissions"
        contextUsage={{ inputTokens: 300, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 }}
        modelContextWindow={1000}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toHaveTextContent('Clear Context (30%)')
    expect(screen.getByTestId('plan-bypass-permissions-checkbox')).toBeInTheDocument()
  })

  it('shows only Send feedback when editor has content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        bypassPermissionMode="bypassPermissions"
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-reject-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-clear-context-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-bypass-permissions-checkbox')).not.toBeInTheDocument()
  })

  it('sends clearContext when Clear Context is checked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-clear', 'agent-clear')}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypassPermissionMode="bypassPermissions"
      />
    ))

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const [agentId, bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(agentId).toBe('agent-clear')
    expect(decoded.clearContext).toBe(true)
    expect(decoded.response.response.behavior).toBe('allow')
  })

  it('sends allow response with permissionMode when the bypass switch is checked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypassPermissionMode="bypassPermissions"
      />
    ))

    // Enable bypass permissions, then approve.
    const bypassSwitch = screen.getByTestId('plan-bypass-permissions-checkbox').querySelector('input')!
    fireEvent.click(bypassSwitch)
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

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
        bypassPermissionMode="bypassPermissions"
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

  it('does not show the bypass switch when bypassPermissionMode is not set', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={makeAskState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('plan-bypass-permissions-checkbox')).not.toBeInTheDocument()
  })
})
