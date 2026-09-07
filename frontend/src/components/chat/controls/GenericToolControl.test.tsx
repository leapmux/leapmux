import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GenericToolActions } from '~/components/chat/controls/GenericToolControl'
import { createControlAnswerState } from './types'

function makeRequest(requestId = 'req-1', agentId = 'agent-1'): ControlRequest {
  return {
    requestId,
    agentId,
    payload: {
      request: { tool_name: 'Bash', input: { command: 'ls' } },
    },
  }
}

function permissionPill() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
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

  it('shows Deny, Allow, and the permission pills when no editor content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Deny')
    expect(screen.getByTestId('control-allow-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-permissions-pill-group')).toBeInTheDocument()
    expect(permissionPill().getByRole('radio', { name: 'Default' })).toBeChecked()
    expect(permissionPill().getByRole('radio', { name: 'Bypass permissions' })).not.toBeChecked()
  })

  it('shows only Send feedback when editor has content', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={true}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply: vi.fn() }}
      />
    ))

    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Send feedback')
    expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
  })

  it('sends allow response with original tool input when allow is clicked', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()
    const request = makeRequest('req-10', 'agent-3')

    render(() => (
      <GenericToolActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply }}
      />
    ))

    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-10')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.response.response.updatedInput).toEqual({ command: 'ls' })
    // Default applies nothing -- the answer is the whole decision.
    expect(apply).not.toHaveBeenCalled()
  })

  it('sends deny response when deny is clicked with an empty editor', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <GenericToolActions
        request={makeRequest('req-deny', 'agent-deny')}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(screen.getByTestId('control-deny-btn'))

    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({
      response: { request_id: 'req-deny', response: { behavior: 'deny' } },
    })
  })

  it('sends allow response and applies the bypass preset when Bypass permissions is selected', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()
    const request = makeRequest('req-42', 'agent-7')

    render(() => (
      <GenericToolActions
        request={request}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply,
        }}
      />
    ))

    fireEvent.click(permissionPill().getByRole('radio', { name: 'Bypass permissions' }))
    // The handler AWAITS the allow before applying the preset, so the assertion
    // waits for that microtask -- ordering is the point of the fix.
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    // Verify allow response was sent
    expect(onRespond).toHaveBeenCalledOnce()
    const [bytes] = onRespond.mock.calls[0]
    const decoded = JSON.parse(new TextDecoder().decode(bytes))
    expect(decoded.response.request_id).toBe('req-42')
    expect(decoded.response.response.behavior).toBe('allow')
    expect(decoded.response.response.updatedInput).toEqual({ command: 'ls' })

    // Verify the preset was applied -- the same change the composer menu item makes.
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })
  })

  it('applies the smart preset when Smart permissions is selected', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()

    render(() => (
      <GenericToolActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{
          smart: { sets: { permissionMode: 'auto' } },
          bypass: { sets: { permissionMode: 'bypassPermissions' } },
          apply,
        }}
      />
    ))

    fireEvent.click(permissionPill().getByRole('radio', { name: 'Smart permissions' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })
  })

  it('does not apply a preset when deny is clicked with one selected', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const apply = vi.fn()

    render(() => (
      <GenericToolActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
        presets={{ bypass: { sets: { permissionMode: 'bypassPermissions' } }, apply }}
      />
    ))

    fireEvent.click(permissionPill().getByRole('radio', { name: 'Bypass permissions' }))
    await fireEvent.click(screen.getByTestId('control-deny-btn'))

    expect(apply).not.toHaveBeenCalled()
  })

  it('does not show the permission pills without presets', () => {
    render(() => (
      <GenericToolActions
        request={makeRequest()}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument()
    expect(screen.queryByRole('radiogroup', { name: 'Permissions' })).not.toBeInTheDocument()
  })
})
