import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { ControlRequestActions, ControlRequestContent } from '../ControlRequestBanner'
import { pluginFor } from '../providers/registry'
import { createControlAnswerState } from './types'
import '../providers'

const schema = { type: 'object', required: ['count', 'enabled', 'color'], properties: {
  count: { type: 'integer', title: 'Count', minimum: 0, maximum: 3 },
  enabled: { type: 'boolean', title: 'Enabled' },
  color: { type: 'string', title: 'Color', oneOf: [{ const: 'b', title: 'Blue' }, { const: 'r', title: 'Red' }] },
} }

const providers = [
  [AgentProvider.CLAUDE_CODE, { request: { subtype: 'elicitation', message: 'Choose the settings.', requested_schema: schema } }],
  [AgentProvider.CODEX, { method: 'mcpServer/elicitation/request', params: { mode: 'form', message: 'Choose the settings.', requestedSchema: schema } }],
  [AgentProvider.REASONIX, { method: '_reasonix.io/mcp/request_interaction', params: { mode: 'form', message: 'Choose the settings.', requestedSchema: schema } }],
  [AgentProvider.GOOSE, { method: 'elicitation/create', params: { mode: 'form', message: 'Choose the settings.', requestedSchema: schema } }],
] as const

describe('shared elicitation control', () => {
  it.each([undefined, AgentProvider.CLAUDE_CODE])('renders the request provider when separate metadata gives %s', async (agentProvider) => {
    const request = { requestId: 'early-form', agentId: 'agent', agentProvider: AgentProvider.REASONIX, payload: {
      method: '_reasonix.io/mcp/request_interaction',
      params: { mode: 'form', requestedSchema: { type: 'object', properties: { count: { type: 'integer', title: 'Count' } } } },
    } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={agentProvider} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={agentProvider} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(screen.getByTestId('elicitation-form')).toBeVisible()
    fireEvent.input(screen.getByLabelText('Count'), { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { action: 'accept', content: { count: 0 } } } })
  })

  it('filters a long choice list and can clear an optional answer', async () => {
    const request = { requestId: 'choices', agentId: 'agent', payload: { method: 'elicitation/create', params: {
      mode: 'form',
      requestedSchema: { type: 'object', properties: { color: { type: 'string', title: 'Color', enum: ['blue', 'red', 'green', 'orange', 'yellow', 'pink', 'white', 'black', 'cyan', 'magenta', 'lime', 'brown'] } } },
    } } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    // JSDOM keeps popover children outside its accessibility tree. Browser tests check visibility.
    const trigger = screen.getByRole('button', { name: 'Color' })
    fireEvent.click(trigger)
    fireEvent.input(screen.getByRole('textbox', { name: 'Filter Color', hidden: true }), { target: { value: 'green' } })
    expect(screen.queryByRole('menuitemradio', { name: 'red', hidden: true })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'green', hidden: true }))
    expect(trigger).toHaveTextContent('green')
    fireEvent.click(trigger)
    fireEvent.input(screen.getByRole('textbox', { name: 'Filter Color', hidden: true }), { target: { value: 'no match' } })
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Select an option', hidden: true }))
    expect(trigger).toHaveTextContent('Select an option')
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { content: {} } } })
  })

  it('shows a protected URL and sends approval without form values', async () => {
    const request = { requestId: 'url', agentId: 'agent', payload: { method: 'elicitation/create', params: {
      mode: 'url',
      message: 'Complete the provider setup.',
      url: 'https://example.com/setup?flow=1',
    } } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    const link = screen.getByRole('link', { name: 'https://example.com/setup?flow=1' })
    expect(link).toHaveAttribute('href', 'https://example.com/setup?flow=1')
    expect(link).toHaveAttribute(UNTRUSTED_LINK_ATTRIBUTE)
    expect(link).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    expect(screen.queryByTestId('elicitation-form')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'url', response: { action: 'accept' } },
    })
  })

  it.each(['javascript:alert(1)', 'file:///private/settings', 'https://user:secret@example.com/', ''])('refuses the unusable URL %s and permits rejection', async (url) => {
    const request = { requestId: 'unsafe-url', agentId: 'agent', payload: { method: 'elicitation/create', params: { mode: 'url', message: '', url } } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('The request contains no usable web link.')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { action: 'decline' } } })
  })

  it('permits cancellation of an unknown interaction mode', async () => {
    const request = { requestId: 'future', agentId: 'agent', payload: { method: 'elicitation/create', params: { mode: 'future-mode', message: 'Unknown interaction' } } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(screen.getByText(/This request type is not supported/)).toBeVisible()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { action: 'cancel' } } })
  })

  it('validates multiple choices and omits an untouched optional field', async () => {
    const request = { requestId: 'multiple', agentId: 'agent', payload: { method: 'elicitation/create', params: { mode: 'form', requestedSchema: {
      type: 'object',
      required: ['colors'],
      properties: {
        colors: { type: 'array', title: 'Colors', minItems: 1, maxItems: 1, items: { type: 'string', enum: ['blue', 'red'] } },
        note: { type: 'string', title: 'Note' },
      },
    } } } }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    const approve = screen.getByRole('button', { name: 'Approve' })
    expect(approve).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'blue' }))
    expect(approve).toBeEnabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'red', hidden: true }))
    expect(approve).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'blue' }))
    fireEvent.click(approve)
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { action: 'accept', content: { colors: ['red'] } } } })
    expect(new TextDecoder().decode(onRespond.mock.calls[0][0])).not.toContain('note')
  })

  it('shows Codex tool arguments and honors the offered approval duration', async () => {
    const request = { requestId: 'mcp-approval', agentId: 'agent', payload: {
      method: 'mcpServer/elicitation/request',
      params: { mode: 'form', message: 'Allow this tool?', requestedSchema: { type: 'object', properties: {} }, _meta: {
        codex_approval_kind: 'mcp_tool_call',
        tool_params: { path: 'sample.py', count: 0 },
        persist: ['session', 'always'],
      } },
    } }
    const state = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={state} agentProvider={AgentProvider.CODEX} />
        <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.CODEX} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(screen.getByText(/sample.py/)).toBeVisible()
    fireEvent.click(screen.getByRole('radio', { name: 'Session' }))
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toMatchObject({ response: { response: { action: 'accept', _meta: { persist: 'session' } } } })
  })

  it.each(providers)('renders the saved answer for provider %s with native field labels', (provider, payload) => {
    const result = { action: 'accept', content: { count: 0, enabled: false, color: 'b' } }
    const response = provider === AgentProvider.CLAUDE_CODE ? { response: { response: result } } : { result }
    const display = pluginFor(provider)?.controlResponseDisplay?.({ provider: '', requestId: 'form', request: payload, response })
    expect(display).toEqual({ kind: 'label', text: 'Approved\nCount: 0\nEnabled: No\nColor: Blue' })
  })

  it.each(providers)('renders and answers provider %s through the same form', async (agentProvider, payload) => {
    const request = { requestId: '001', agentId: 'agent', payload }
    const answerState = createControlAnswerState()
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={agentProvider} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={agentProvider} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Count *'), { target: { value: '0' } })
    expect(screen.queryAllByRole('combobox')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Enabled *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'No', hidden: true }))
    fireEvent.click(screen.getByRole('button', { name: 'Color *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Blue', hidden: true }))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toEqual({ type: 'control_response', response: { subtype: 'success', request_id: '001', response: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } } })
  })

  it('retains the form and permits retry after delivery fails', async () => {
    const request = { requestId: 'form', agentId: 'agent', payload: { method: 'elicitation/create', params: { mode: 'form', requestedSchema: { type: 'object', properties: {} } } } }
    const onRespond = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined)
    render(() => <ControlRequestActions request={request} answerState={createControlAnswerState()} agentProvider={AgentProvider.GOOSE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not send'))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(onRespond).toHaveBeenCalledTimes(2))
    expect(new TextDecoder().decode(onRespond.mock.calls[1][0])).not.toContain('content')
  })
})
