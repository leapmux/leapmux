import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GenericToolActions, GenericToolContent } from '~/components/chat/controls/GenericToolControl'
import { prettifyJson } from '~/lib/jsonFormat'
import { permissionPillGroup } from '~/test-support/controlRequests'
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
    // No preset is on, so the group opens on the pill that changes nothing.
    expect(permissionPillGroup().getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    expect(permissionPillGroup().getByRole('radio', { name: 'Bypass' })).not.toBeChecked()
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
    // Unchanged applies nothing -- the answer is the whole decision.
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

  it('sends allow response and applies the bypass preset when Bypass is selected', async () => {
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

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
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

  it('applies the smart preset when Smart is selected', async () => {
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

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Smart' }))
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })
  })

  it('opens on the preset the session has on', async () => {
    // The session runs on bypass, so the group opens there and an Allow keeps
    // it there rather than dropping the session back to asking.
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
          active: 'bypass',
        }}
      />
    ))

    expect(permissionPillGroup().getByRole('radio', { name: 'Bypass' })).toBeChecked()
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })
  })

  it('applies nothing when no preset is on', async () => {
    // An ordinary request must never turn a preset ON by itself, so an
    // untouched group leaves the agent's permission mode alone.
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

    expect(permissionPillGroup().getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    await fireEvent.click(screen.getByTestId('control-allow-btn'))

    expect(apply).not.toHaveBeenCalled()
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

    fireEvent.click(permissionPillGroup().getByRole('radio', { name: 'Bypass' }))
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

  it('describes that the selected preset applies on allow', () => {
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

    expect(screen.getByRole('radiogroup', { name: 'Permissions' }))
      .toHaveAccessibleDescription('The selected preset applies when you allow or approve this request')
  })
})

const PERMISSION_REQUIRED_RE = /Permission Required:/
const BASH_RE = /Bash/
const KEY_19_RE = /key_19/

function makeContentRequest(input: Record<string, unknown>): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      request: { tool_name: 'Bash', input },
    },
  }
}

describe('genericToolContent', () => {
  it('uses Fractured JSON for tool arguments', () => {
    const request = makeRequest()
    const { container } = render(() => <GenericToolContent request={request} />)
    expect(container.querySelector('pre')?.textContent).toBe(prettifyJson({ command: 'ls' }))
  })

  it('renders tool name and short JSON without toggle', () => {
    render(() => <GenericToolContent request={makeContentRequest({ command: 'ls' })} />)

    expect(screen.getByText(PERMISSION_REQUIRED_RE)).toBeInTheDocument()
    expect(screen.getByText(BASH_RE)).toBeInTheDocument()
    // Short JSON needs no expansion control.
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('truncates long JSON and shows toggle', () => {
    const longInput: Record<string, string> = {}
    for (let i = 0; i < 20; i++) {
      longInput[`key_${i}`] = `value_${i}`
    }
    render(() => <GenericToolContent request={makeContentRequest(longInput)} />)

    const toggle = screen.getByRole('button')
    expect(toggle).toHaveTextContent('more line')
  })

  it('expands long JSON when toggle is clicked', () => {
    const longInput: Record<string, string> = {}
    for (let i = 0; i < 20; i++) {
      longInput[`key_${i}`] = `value_${i}`
    }
    render(() => <GenericToolContent request={makeContentRequest(longInput)} />)

    fireEvent.click(screen.getByRole('button'))

    // Expansion shows every key.
    expect(screen.getByText(KEY_19_RE)).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })
})
