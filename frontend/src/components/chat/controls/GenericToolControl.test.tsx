import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GenericToolActions } from '~/components/chat/controls/GenericToolControl'
import { createAskQuestionState } from '~/test-support/askQuestionState'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'Bash', input: { command: 'ls' } },
    },
  }
}

describe('genericToolActions', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('shows Deny, Allow, and the Bypass Permissions switch when no editor content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Deny')
    expect(screen.getByTestId('control-allow-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-bypass-permissions-checkbox')).toBeInTheDocument()
  })

  it('shows only Send feedback when editor has content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-bypass-permissions-checkbox')).not.toBeInTheDocument()
  })

  it('sends allow response with original tool input when allow is clicked', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const request = makeRequest('req-10', 'agent-3')

    render(() => (
      <GenericToolActions
        request={request}
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-3')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-10')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.response.response.updatedInput).toEqual({ command: 'ls' })
  })

  it('sends deny response when deny is clicked with an empty editor', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <GenericToolActions
        request={makeRequest('req-deny', 'agent-deny')}
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(screen.getByTestId('control-deny-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-deny')
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      response: { request_id: 'req-deny', response: { behavior: 'deny' } },
    })
  })

  it('sends allow response and changes permission mode when bypass is clicked', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const applyBypass = vi.fn()
    const request = makeRequest('req-42', 'agent-7')

    render(() => (
      <GenericToolActions
        request={request}
        askState={createAskQuestionState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        bypass={{ settings: { sets: { permissionMode: 'bypassPermissions' } }, apply: applyBypass }}
      />
    ))

    // The handler AWAITS the allow before switching the mode, so the assertion
    // waits for that microtask -- ordering is the point of the fix.
    fireEvent.click(screen.getByTestId('control-bypass-permissions-checkbox').querySelector('input')!)
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    // Verify allow response was sent
    expect(onRespond).toHaveBeenCalledOnce()
    const [agentId, bytes] = onRespond.mock.calls[0]
    expect(agentId).toBe('agent-7')
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.response.response.updatedInput).toEqual({ command: 'ls' })

    // Verify permission mode change
    expect(applyBypass).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })
  })

  it('does not show the bypass switch without a bypass mode', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        askState={createAskQuestionState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('control-bypass-permissions-checkbox')).not.toBeInTheDocument()
  })
})
