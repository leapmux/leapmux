import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'
import { createAskQuestionState } from '~/test-support/askQuestionState'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

describe('exitPlanModeActions', () => {
  it('shows Reject, Approve, and the plan switches when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
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
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
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
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
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
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
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

  // A preset that switches some axis OTHER than the permission mode cannot be applied
  // through this banner at all: the approval travels as one control response, and the
  // only part of a preset that response can carry is the mode. Copilot's bypass sets
  // `allow_all`, so the switch must not be drawn -- drawing it produced a checkbox that
  // silently did nothing once the preset type stopped guaranteeing a permissionMode key.
  it('draws no bypass switch for a preset that carries no permission mode', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-77', 'agent-7')}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { allow_all: 'on' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.queryByTestId('plan-bypass-permissions-checkbox')).toBeNull()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  it('sends allow response without permissionMode for normal approve', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-42', 'agent-5')

    render(() => (
      <ExitPlanModeActions
        request={request}
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
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

  it('does not show the bypass switch when bypass settings are absent', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('plan-bypass-permissions-checkbox')).not.toBeInTheDocument()
  })
})
