import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ExitPlanModeActions } from '~/components/chat/controls/ExitPlanModeControl'
import { createControlAnswerState } from './types'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

function permissionPill() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
}

describe('exitPlanModeActions', () => {
  it('shows Reject, Approve, the Clear Context switch, and the permission pills when no editor content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply: vi.fn(),
        }}
        contextUsage={{ inputTokens: 300, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 }}
        modelContextWindow={1000}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toHaveTextContent('Clear Context (30%)')
    expect(permissionPill().getByRole('radio', { name: 'Default' })).toBeChecked()
    expect(permissionPill().getByRole('radio', { name: 'Smart permissions' })).toBeInTheDocument()
    expect(permissionPill().getByRole('radio', { name: 'Bypass permissions' })).toBeInTheDocument()
  })

  it('shows only Send feedback when editor has content', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('plan-reject-btn')).toBeInTheDocument()
    expect(screen.getByTestId('plan-reject-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-clear-context-checkbox')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
  })

  it('sends clearContext when Clear Context is checked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest('req-clear', 'agent-clear')}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input')!)
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const [bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.clearContext).toBe(true)
    expect(decoded.response.response.behavior).toBe('allow')
  })

  it('sends allow response with the bypass mode when Bypass permissions is selected', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-99', 'agent-3')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    // Select bypass permissions, then approve.
    fireEvent.click(permissionPill().getByRole('radio', { name: 'Bypass permissions' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-99')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.permissionMode).toBe('bypassPermissions')
  })

  it('sends allow response with the smart mode when Smart permissions is selected', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)

    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply: vi.fn(),
        }}
      />
    ))

    fireEvent.click(permissionPill().getByRole('radio', { name: 'Smart permissions' }))
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes)).permissionMode).toBe('auto')
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
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { allow_all: 'on' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).toBeNull()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })

  it('sends allow response without permissionMode for normal approve', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-42', 'agent-5')

    render(() => (
      <ExitPlanModeActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.permissionMode).toBeUndefined()
  })

  it('does not show the permission pills when presets are absent', () => {
    render(() => (
      <ExitPlanModeActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.getByTestId('plan-clear-context-checkbox')).toBeInTheDocument()
  })
})
